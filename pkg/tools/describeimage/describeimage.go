package describeimage

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"unicode"

	toolscommon "github.com/richardartoul/swarmd/pkg/tools/common"
	toolscore "github.com/richardartoul/swarmd/pkg/tools/core"
	toolregistry "github.com/richardartoul/swarmd/pkg/tools/registry"
)

const (
	toolName                 = "describe_image"
	maxDescribeImageBytes    = 8 << 20
	defaultDescribeImageText = "Describe this image in detail, including any visible text."
)

var (
	registerOnce        sync.Once
	supportedMediaTypes = map[string]struct{}{
		"image/gif":  {},
		"image/jpeg": {},
		"image/png":  {},
		"image/webp": {},
	}
)

type args struct {
	FilePath    string `json:"file_path"`
	ImageBase64 string `json:"image_base64"`
	ImageURL    string `json:"image_url"`
	MediaType   string `json:"media_type"`
	Prompt      string `json:"prompt"`
}

type plugin struct{}

func init() {
	Register()
}

func Register() {
	registerOnce.Do(func() {
		toolregistry.MustRegister(plugin{}, toolregistry.RegistrationOptions{BuiltIn: true})
	})
}

func (plugin) Definition() toolscore.ToolDefinition {
	parameters := toolscommon.ObjectSchema(map[string]any{
		"file_path":    toolscommon.StringSchema("Absolute or working-directory-relative path to an image file. Provide exactly one of file_path, image_base64, or image_url."),
		"image_base64": toolscommon.StringSchema("Inline base64-encoded image payload. Provide exactly one of file_path, image_base64, or image_url. Data URLs are also accepted."),
		"image_url":    toolscommon.StringSchema("Absolute public http or https URL for an image the provider can fetch directly. Provide exactly one of file_path, image_base64, or image_url."),
		"media_type":   toolscommon.StringSchema("Optional image media type such as image/png. Required for plain base64 payloads unless image_base64 is a data URL. Rejected when image_url is used."),
		"prompt":       toolscommon.StringSchema("Optional instruction that explains what to focus on while describing the image."),
	})
	parameters["oneOf"] = []map[string]any{
		{"required": []string{"file_path"}},
		{"required": []string{"image_base64"}},
		{"required": []string{"image_url"}},
	}
	return toolscore.ToolDefinition{
		Name:        toolName,
		Description: "Describes an image using the active model provider's native image-understanding API.",
		Kind:        toolscore.ToolKindFunction,
		Parameters:  parameters,
		Examples: []string{
			`{"file_path":"/workspace/screenshot.png"}`,
			`{"image_base64":"iVBORw0KGgoAAAANSUhEUgAAAAUA...","media_type":"image/png","prompt":"Summarize the chart."}`,
			`{"image_base64":"data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAUA...","prompt":"Read the visible text."}`,
			`{"image_url":"https://example.com/screenshot.png","prompt":"Describe the UI state."}`,
		},
		OutputNotes: "Returns a normalized image description. Supports PNG, JPEG, GIF, and WebP inputs from a file path, inline base64 payload, or public image URL.",
		Interop: toolscommon.ToolInterop(
			toolName,
			toolscore.ToolBoundaryKindFunction,
			toolscore.ToolBoundaryKindFunction,
			toolName,
		),
		SafetyTags: []string{"read_only"},
		ReadOnly:   true,
	}
}

func (plugin) NewHandler(config toolscore.ConfiguredTool) (toolscore.ToolHandler, error) {
	if err := toolscommon.ValidateNoToolConfig(toolName, config.Config); err != nil {
		return nil, err
	}
	return toolscore.ToolHandlerFunc(handle), nil
}

func handle(ctx context.Context, toolCtx toolscore.ToolContext, step *toolscore.Step, call *toolscore.ToolAction) error {
	args, err := toolscore.DecodeToolInput[args](call.Input)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	request, err := buildRequest(toolCtx, args)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	response, err := toolCtx.DescribeImage(ctx, request)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	description := strings.TrimSpace(response.Description)
	if description == "" {
		toolCtx.SetPolicyError(step, fmt.Errorf("describe_image backend returned an empty description"))
		return nil
	}
	var b strings.Builder
	if provider := strings.TrimSpace(response.Provider); provider != "" {
		fmt.Fprintf(&b, "Provider: %s\n", provider)
	}
	if model := strings.TrimSpace(response.Model); model != "" {
		fmt.Fprintf(&b, "Model: %s\n", model)
	}
	b.WriteString("Description:\n")
	b.WriteString(description)
	if !strings.HasSuffix(description, "\n") {
		b.WriteString("\n")
	}
	toolCtx.SetOutput(step, b.String())
	return nil
}

func buildRequest(toolCtx toolscore.ToolContext, args args) (toolscore.ImageDescriptionRequest, error) {
	prompt := strings.TrimSpace(args.Prompt)
	if prompt == "" {
		prompt = defaultDescribeImageText
	}
	filePath := strings.TrimSpace(args.FilePath)
	imageBase64 := strings.TrimSpace(args.ImageBase64)
	imageURL := strings.TrimSpace(args.ImageURL)
	sourceCount := 0
	for _, source := range []string{filePath, imageBase64, imageURL} {
		if source != "" {
			sourceCount++
		}
	}
	switch {
	case sourceCount == 0:
		return toolscore.ImageDescriptionRequest{}, fmt.Errorf("exactly one of file_path, image_base64, or image_url must be provided")
	case sourceCount > 1:
		return toolscore.ImageDescriptionRequest{}, fmt.Errorf("file_path, image_base64, and image_url are mutually exclusive")
	}
	if filePath != "" {
		return requestFromFilePath(toolCtx, filePath, strings.TrimSpace(args.MediaType), prompt)
	}
	if imageBase64 != "" {
		return requestFromBase64(strings.TrimSpace(args.MediaType), imageBase64, prompt)
	}
	return requestFromImageURL(strings.TrimSpace(args.MediaType), imageURL, prompt)
}

func requestFromFilePath(toolCtx toolscore.ToolContext, filePath, declaredMediaType, prompt string) (toolscore.ImageDescriptionRequest, error) {
	if toolCtx == nil {
		return toolscore.ImageDescriptionRequest{}, fmt.Errorf("tool context is unavailable")
	}
	resolved, err := toolCtx.ResolvePath(filePath)
	if err != nil {
		return toolscore.ImageDescriptionRequest{}, err
	}
	info, err := toolCtx.FileSystem().Stat(resolved)
	if err != nil {
		return toolscore.ImageDescriptionRequest{}, err
	}
	if info.IsDir() {
		return toolscore.ImageDescriptionRequest{}, fmt.Errorf("%q is a directory", resolved)
	}
	data, err := readFileLimited(toolCtx, resolved, maxDescribeImageBytes)
	if err != nil {
		return toolscore.ImageDescriptionRequest{}, err
	}
	mediaType, err := detectImageMediaType(data)
	if err != nil {
		return toolscore.ImageDescriptionRequest{}, err
	}
	if declaredMediaType != "" {
		normalizedDeclared, err := normalizeImageMediaType(declaredMediaType)
		if err != nil {
			return toolscore.ImageDescriptionRequest{}, err
		}
		if normalizedDeclared != mediaType {
			return toolscore.ImageDescriptionRequest{}, fmt.Errorf("declared media_type %q does not match detected media type %q", normalizedDeclared, mediaType)
		}
	}
	return toolscore.ImageDescriptionRequest{
		Prompt:    prompt,
		MediaType: mediaType,
		Data:      data,
	}, nil
}

func requestFromBase64(declaredMediaType, rawPayload, prompt string) (toolscore.ImageDescriptionRequest, error) {
	mediaType := ""
	payload := rawPayload
	if strings.HasPrefix(strings.ToLower(payload), "data:") {
		parsedMediaType, parsedPayload, err := parseDataURL(payload)
		if err != nil {
			return toolscore.ImageDescriptionRequest{}, err
		}
		mediaType = parsedMediaType
		payload = parsedPayload
	}
	if mediaType == "" {
		if strings.TrimSpace(declaredMediaType) == "" {
			return toolscore.ImageDescriptionRequest{}, fmt.Errorf("media_type is required when image_base64 is not a data URL")
		}
		normalized, err := normalizeImageMediaType(declaredMediaType)
		if err != nil {
			return toolscore.ImageDescriptionRequest{}, err
		}
		mediaType = normalized
	} else if strings.TrimSpace(declaredMediaType) != "" {
		normalized, err := normalizeImageMediaType(declaredMediaType)
		if err != nil {
			return toolscore.ImageDescriptionRequest{}, err
		}
		if normalized != mediaType {
			return toolscore.ImageDescriptionRequest{}, fmt.Errorf("declared media_type %q does not match data URL media type %q", normalized, mediaType)
		}
	}
	data, err := decodeBase64Payload(payload)
	if err != nil {
		return toolscore.ImageDescriptionRequest{}, err
	}
	if len(data) > maxDescribeImageBytes {
		return toolscore.ImageDescriptionRequest{}, fmt.Errorf("decoded image exceeded the read limit of %d bytes", maxDescribeImageBytes)
	}
	detectedMediaType, err := detectImageMediaType(data)
	if err != nil {
		return toolscore.ImageDescriptionRequest{}, err
	}
	if detectedMediaType != mediaType {
		return toolscore.ImageDescriptionRequest{}, fmt.Errorf("declared media_type %q does not match detected media type %q", mediaType, detectedMediaType)
	}
	return toolscore.ImageDescriptionRequest{
		Prompt:    prompt,
		MediaType: mediaType,
		Data:      data,
	}, nil
}

func requestFromImageURL(declaredMediaType, rawURL, prompt string) (toolscore.ImageDescriptionRequest, error) {
	if strings.TrimSpace(declaredMediaType) != "" {
		return toolscore.ImageDescriptionRequest{}, fmt.Errorf("media_type is only supported with file_path or image_base64 inputs")
	}
	imageURL, err := normalizePublicImageURL(rawURL)
	if err != nil {
		return toolscore.ImageDescriptionRequest{}, err
	}
	return toolscore.ImageDescriptionRequest{
		Prompt:   prompt,
		ImageURL: imageURL,
	}, nil
}

func readFileLimited(toolCtx toolscore.ToolContext, path string, limit int64) ([]byte, error) {
	file, err := toolCtx.FileSystem().Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if limit <= 0 {
		return nil, fmt.Errorf("read limit must be positive")
	}
	reader := io.LimitReader(file, limit+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%q exceeded the read limit of %d bytes", path, limit)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("%q is empty", path)
	}
	return data, nil
}

func parseDataURL(raw string) (string, string, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(strings.ToLower(raw), "data:") {
		return "", "", fmt.Errorf("image_base64 data URL must start with data:")
	}
	headerAndPrefix, payload, ok := strings.Cut(raw, ",")
	if !ok {
		return "", "", fmt.Errorf("image_base64 data URL must contain a comma separator")
	}
	header := headerAndPrefix[len("data:"):]
	lowerHeader := strings.ToLower(header)
	if !strings.HasSuffix(lowerHeader, ";base64") {
		return "", "", fmt.Errorf("image_base64 data URL must use base64 encoding")
	}
	mediaTypePart := strings.TrimSpace(header[:len(header)-len(";base64")])
	if mediaTypePart == "" {
		return "", "", fmt.Errorf("image_base64 data URL must include an image media type")
	}
	mediaType, err := normalizeImageMediaType(mediaTypePart)
	if err != nil {
		return "", "", err
	}
	return mediaType, payload, nil
}

func decodeBase64Payload(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("image_base64 must not be empty")
	}
	raw = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, raw)
	encodings := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	var lastErr error
	for _, encoding := range encodings {
		data, err := encoding.DecodeString(raw)
		if err == nil {
			if len(data) == 0 {
				return nil, fmt.Errorf("image_base64 decoded to empty data")
			}
			return data, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("image_base64 is not valid base64: %w", lastErr)
}

func detectImageMediaType(data []byte) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("image data must not be empty")
	}
	sniffLen := len(data)
	if sniffLen > 512 {
		sniffLen = 512
	}
	return normalizeImageMediaType(http.DetectContentType(data[:sniffLen]))
}

func normalizeImageMediaType(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("media_type must not be empty")
	}
	mediaType, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return "", fmt.Errorf("media_type %q is invalid: %w", raw, err)
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	switch mediaType {
	case "image/jpg", "image/pjpeg":
		mediaType = "image/jpeg"
	}
	if _, ok := supportedMediaTypes[mediaType]; !ok {
		return "", fmt.Errorf("media_type %q is not supported; supported types are image/png, image/jpeg, image/gif, and image/webp", mediaType)
	}
	return mediaType, nil
}

func normalizePublicImageURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("image_url must not be empty")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("image_url %q is invalid: %w", raw, err)
	}
	if !parsed.IsAbs() || parsed.Host == "" {
		return "", fmt.Errorf("image_url must be an absolute http or https URL")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return "", fmt.Errorf("image_url must use http or https")
	}
	if parsed.User != nil {
		return "", fmt.Errorf("image_url must not include embedded credentials")
	}
	hostname := strings.TrimSpace(parsed.Hostname())
	if hostname == "" {
		return "", fmt.Errorf("image_url must include a hostname")
	}
	lowerHostname := strings.ToLower(hostname)
	switch {
	case lowerHostname == "localhost":
		return "", fmt.Errorf("image_url must be publicly reachable, not localhost")
	case strings.HasSuffix(lowerHostname, ".localhost"), strings.HasSuffix(lowerHostname, ".local"):
		return "", fmt.Errorf("image_url must be publicly reachable, not a local hostname")
	}
	if addr, err := netip.ParseAddr(hostname); err == nil {
		if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
			return "", fmt.Errorf("image_url must be publicly reachable, not a private or local IP address")
		}
	}
	return parsed.String(), nil
}
