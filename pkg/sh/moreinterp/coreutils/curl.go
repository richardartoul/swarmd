package coreutils

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/richardartoul/swarmd/pkg/sh/internal"
	"github.com/richardartoul/swarmd/pkg/sh/interp"
)

func runCurl(env *commandEnv, args []string) error {
	var (
		requestMethod   string
		outputPath      string
		dumpHeaderPath  string
		userAgent       string
		writeOutFormat  string
		includeHeaders  bool
		headOnly        bool
		followLocation  bool
		locationTrusted bool
		post301         bool
		post302         bool
		post303         bool
		failOnHTTPError bool
		silent          bool
		showError       bool
		maxTimeSeconds  float64
		connectTimeout  float64
		maxRedirects    = defaultCurlMaxRedirects
		headers         []string
		dataParts       []curlDataPart
	)

	parsedOpts, operands, err := parseUtilityOptions(args, []internal.OptionSpec{
		{Canonical: "request", Names: []string{"-X", "--request"}, ValueMode: internal.RequiredOptionValue},
		{Canonical: "output", Names: []string{"-o", "--output"}, ValueMode: internal.RequiredOptionValue},
		{Canonical: "dump-header", Names: []string{"-D", "--dump-header"}, ValueMode: internal.RequiredOptionValue},
		{Canonical: "user-agent", Names: []string{"-A", "--user-agent"}, ValueMode: internal.RequiredOptionValue},
		{Canonical: "write-out", Names: []string{"-w", "--write-out"}, ValueMode: internal.RequiredOptionValue},
		{Canonical: "include", Names: []string{"-i", "--include", "--show-headers"}},
		{Canonical: "head", Names: []string{"-I", "--head"}},
		{Canonical: "location", Names: []string{"-L", "--location"}},
		{Canonical: "location-trusted", Names: []string{"--location-trusted"}},
		{Canonical: "post301", Names: []string{"--post301"}},
		{Canonical: "post302", Names: []string{"--post302"}},
		{Canonical: "post303", Names: []string{"--post303"}},
		{Canonical: "fail", Names: []string{"-f", "--fail"}},
		{Canonical: "silent", Names: []string{"-s", "--silent"}},
		{Canonical: "show-error", Names: []string{"-S", "--show-error"}},
		{Canonical: "max-time", Names: []string{"-m", "--max-time"}, ValueMode: internal.RequiredOptionValue},
		{Canonical: "max-redirs", Names: []string{"--max-redirs"}, ValueMode: internal.RequiredOptionValue},
		{Canonical: "connect-timeout", Names: []string{"--connect-timeout"}, ValueMode: internal.RequiredOptionValue},
		{Canonical: "header", Names: []string{"-H", "--header"}, ValueMode: internal.RequiredOptionValue},
		{Canonical: "data", Names: []string{"-d", "--data", "--data-ascii"}, ValueMode: internal.RequiredOptionValue},
		{Canonical: "data-binary", Names: []string{"--data-binary"}, ValueMode: internal.RequiredOptionValue},
	})
	if err != nil {
		return err
	}
	for _, opt := range parsedOpts {
		switch opt.Canonical {
		case "request":
			requestMethod = opt.Value
		case "output":
			outputPath = opt.Value
		case "dump-header":
			dumpHeaderPath = opt.Value
		case "user-agent":
			userAgent = opt.Value
		case "write-out":
			writeOutFormat = opt.Value
		case "include":
			includeHeaders = true
		case "head":
			headOnly = true
		case "location":
			followLocation = true
		case "location-trusted":
			followLocation = true
			locationTrusted = true
		case "post301":
			post301 = true
		case "post302":
			post302 = true
		case "post303":
			post303 = true
		case "fail":
			failOnHTTPError = true
		case "silent":
			silent = true
		case "show-error":
			showError = true
		case "max-time":
			maxTimeSeconds, err = strconv.ParseFloat(opt.Value, 64)
			if err != nil {
				return fmt.Errorf("curl: invalid max-time %q", opt.Value)
			}
		case "max-redirs":
			maxRedirects, err = strconv.Atoi(opt.Value)
			if err != nil || maxRedirects < -1 {
				return fmt.Errorf("curl: invalid max-redirs %q", opt.Value)
			}
		case "connect-timeout":
			connectTimeout, err = strconv.ParseFloat(opt.Value, 64)
			if err != nil {
				return fmt.Errorf("curl: invalid connect-timeout %q", opt.Value)
			}
		case "header":
			headers = append(headers, opt.Value)
		case "data":
			dataParts = append(dataParts, curlDataPart{
				mode:  curlDataModeForm,
				value: opt.Value,
			})
		case "data-binary":
			dataParts = append(dataParts, curlDataPart{
				mode:  curlDataModeBinary,
				value: opt.Value,
			})
		}
	}
	if len(operands) != 1 {
		return fmt.Errorf("curl expects exactly one URL")
	}
	if headOnly && len(dataParts) > 0 {
		return fmt.Errorf("curl: --head is mutually exclusive with --data, --data-ascii and --data-binary")
	}

	explicitMethod := requestMethod != ""
	method := requestMethod
	if method == "" {
		switch {
		case headOnly:
			method = http.MethodHead
		case len(dataParts) > 0:
			method = http.MethodPost
		default:
			method = http.MethodGet
		}
	}

	var requestBody []byte
	if len(dataParts) > 0 {
		requestBody, err = curlRequestBody(env, dataParts)
		if err != nil {
			return err
		}
	}

	requestCtx := env.ctx
	if maxTimeSeconds > 0 {
		var cancel context.CancelFunc
		requestCtx, cancel = context.WithTimeout(requestCtx, secondsDuration(maxTimeSeconds))
		defer cancel()
	}

	var bodyReader io.Reader
	if requestBody != nil {
		bodyReader = bytes.NewReader(requestBody)
	}
	request, err := http.NewRequestWithContext(requestCtx, method, operands[0], bodyReader)
	if err != nil {
		return err
	}
	for _, header := range headers {
		name, value, ok := strings.Cut(header, ":")
		if !ok {
			return fmt.Errorf("invalid header %q", header)
		}
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if strings.EqualFold(name, "Host") {
			request.Host = value
			continue
		}
		request.Header.Add(name, value)
	}
	if userAgent != "" {
		request.Header.Set("User-Agent", userAgent)
	}
	if len(requestBody) > 0 && request.Header.Get("Content-Type") == "" {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	baseHeaders := request.Header.Clone()

	clientFactory := env.hc.HTTPClientFactory
	if clientFactory == nil {
		var err error
		clientFactory, err = interp.NewHTTPClientFactory(env.hc.NetworkDialer, nil)
		if err != nil {
			return err
		}
	}
	client := clientFactory.NewClient(interp.HTTPClientOptions{
		ConnectTimeout:  secondsDuration(connectTimeout),
		FollowRedirects: false,
	})
	debugEnabled := curlDebugEnabled()

	var (
		writer            io.Writer
		closeWriter       io.Closer
		headerWriter      io.Writer
		closeHeaderWriter io.Closer
	)
	defer func() {
		if closeHeaderWriter != nil {
			closeHeaderWriter.Close()
		}
		if closeWriter != nil {
			closeWriter.Close()
		}
	}()

	ensureWriter := func() error {
		if writer != nil {
			return nil
		}
		writer, closeWriter, err = curlOutputWriter(env, outputPath)
		return err
	}
	ensureHeaderWriter := func() error {
		if dumpHeaderPath == "" || headerWriter != nil {
			return nil
		}
		if dumpHeaderPath == outputPath {
			if err := ensureWriter(); err != nil {
				return err
			}
			headerWriter = writer
			return nil
		}
		headerWriter, closeHeaderWriter, err = curlOutputWriter(env, dumpHeaderPath)
		return err
	}
	writeRequestedHeaders := func(response *http.Response) error {
		if dumpHeaderPath != "" {
			if err := ensureHeaderWriter(); err != nil {
				return err
			}
			if err := writeCurlHeaders(headerWriter, response); err != nil {
				return err
			}
		}
		if includeHeaders || headOnly {
			if err := ensureWriter(); err != nil {
				return err
			}
			if err := writeCurlHeaders(writer, response); err != nil {
				return err
			}
		}
		return nil
	}

	redirectOpts := curlRedirectOptions{
		followLocation:  followLocation,
		locationTrusted: locationTrusted,
		post301:         post301,
		post302:         post302,
		post303:         post303,
		maxRedirects:    maxRedirects,
		explicitMethod:  explicitMethod,
		initialURL:      request.URL,
	}
	currentRequest := request
	currentBody := requestBody
	redirectsFollowed := 0
	startedAt := time.Now()
	var totalUploadedBytes int64

	for {
		requestBodyBytes := int64(len(currentBody))
		requestToSend := curlRequestWithHeaderTrace(currentRequest, debugEnabled)
		response, err := client.Do(requestToSend)
		if err != nil {
			curlDebugPrintRequestObject(requestToSend, debugEnabled)
			result := newCurlTransferResult(nil, requestToSend.URL, redirectsFollowed, totalUploadedBytes, 0, startedAt, err)
			if err := writeCurlWriteOut(env.stdout(), result, writeOutFormat); err != nil {
				return err
			}
			return curlMaybeShowError(env, silent, showError, err)
		}
		totalUploadedBytes += requestBodyBytes
		debugRequest := requestToSend
		if response.Request != nil {
			debugRequest = response.Request
		}
		curlDebugPrintRequestObject(debugRequest, debugEnabled)
		curlDebugPrintResponseStatus(response, debugEnabled)

		nextRequest, nextBody, shouldRedirect, redirectErr := curlPrepareRedirectRequest(
			requestCtx,
			requestToSend,
			response,
			currentBody,
			baseHeaders,
			redirectOpts,
			redirectsFollowed,
		)
		if redirectErr != nil {
			if err := writeRequestedHeaders(response); err != nil {
				response.Body.Close()
				return err
			}
			if err := curlDebugPrintResponseBodyFromReader(response.Body, debugEnabled); err != nil {
				response.Body.Close()
				return err
			}
			result := newCurlTransferResult(response, requestToSend.URL, redirectsFollowed, totalUploadedBytes, 0, startedAt, redirectErr)
			response.Body.Close()
			if err := writeCurlWriteOut(env.stdout(), result, writeOutFormat); err != nil {
				return err
			}
			return curlMaybeShowError(env, silent, showError, redirectErr)
		}
		if !shouldRedirect {
			defer response.Body.Close()
			var bodyBytesWritten int64

			if failOnHTTPError && response.StatusCode >= http.StatusBadRequest {
				if err := writeRequestedHeaders(response); err != nil {
					return err
				}
				if err := curlDebugPrintResponseBodyFromReader(response.Body, debugEnabled); err != nil {
					return err
				}
				if !debugEnabled {
					_, _ = io.Copy(io.Discard, response.Body)
				}
				transferErr := newCurlStatusError(curlExitHTTPPageNotRetrieved, "curl: server returned HTTP %d", response.StatusCode)
				result := newCurlTransferResult(response, requestToSend.URL, redirectsFollowed, totalUploadedBytes, 0, startedAt, transferErr)
				if err := writeCurlWriteOut(env.stdout(), result, writeOutFormat); err != nil {
					return err
				}
				return curlMaybeShowError(env, silent, showError, transferErr)
			}
			if err := writeRequestedHeaders(response); err != nil {
				return err
			}
			if !headOnly {
				if err := ensureWriter(); err != nil {
					return err
				}
				if debugEnabled {
					responseBody, err := io.ReadAll(response.Body)
					if err != nil {
						return err
					}
					curlDebugPrintResponseBody(responseBody, true)
					bodyBytesWritten, err = io.Copy(writer, bytes.NewReader(responseBody))
					if err != nil {
						return err
					}
				} else {
					bodyBytesWritten, err = io.Copy(writer, response.Body)
					if err != nil {
						return err
					}
				}
			} else {
				curlDebugPrintResponseBody(nil, debugEnabled)
			}
			result := newCurlTransferResult(response, requestToSend.URL, redirectsFollowed, totalUploadedBytes, bodyBytesWritten, startedAt, nil)
			return writeCurlWriteOut(env.stdout(), result, writeOutFormat)
		}

		if err := writeRequestedHeaders(response); err != nil {
			response.Body.Close()
			return err
		}
		if err := curlDebugPrintResponseBodyFromReader(response.Body, debugEnabled); err != nil {
			response.Body.Close()
			return err
		}
		closeCurlRedirectResponse(response)

		currentRequest = nextRequest
		currentBody = nextBody
		redirectsFollowed++
	}
}

func curlRequestWithHeaderTrace(request *http.Request, enabled bool) *http.Request {
	if request == nil {
		return nil
	}
	if !enabled {
		return request
	}
	var (
		wrotePrefix bool
		wroteHeader bool
	)
	printPrefix := func() {
		if wrotePrefix {
			return
		}
		wrotePrefix = true
		println("1. Headers")
		println("Request:", request.Method, request.URL.String())
	}
	trace := &httptrace.ClientTrace{
		WroteHeaderField: func(name string, values []string) {
			printPrefix()
			wroteHeader = true
			for _, value := range values {
				println(name+":", value)
			}
		},
		WroteHeaders: func() {
			printPrefix()
			if wroteHeader {
				return
			}
			println("(no headers)")
		},
	}
	return request.WithContext(httptrace.WithClientTrace(request.Context(), trace))
}

func curlDebugEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CURL_DEBUG_HEADERS"))) {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func curlDebugPrintRequestObject(request *http.Request, enabled bool) {
	if !enabled || request == nil {
		return
	}
	println("2. Request object")
	println(fmt.Sprintf("%+v", *request))
}

func curlDebugPrintResponseStatus(response *http.Response, enabled bool) {
	if !enabled || response == nil {
		return
	}
	println("3. Response status code")
	println(response.StatusCode)
}

func curlDebugPrintResponseBodyFromReader(reader io.Reader, enabled bool) error {
	if !enabled {
		return nil
	}
	if reader == nil {
		curlDebugPrintResponseBody(nil, true)
		return nil
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	curlDebugPrintResponseBody(body, true)
	return nil
}

func curlDebugPrintResponseBody(body []byte, enabled bool) {
	if !enabled {
		return
	}
	println("4. Response status body")
	if len(body) == 0 {
		println("(empty)")
		return
	}
	println(string(body))
}

func curlRequestBody(env *commandEnv, parts []curlDataPart) ([]byte, error) {
	var body bytes.Buffer
	for i, part := range parts {
		if i > 0 {
			body.WriteByte('&')
		}
		payload, err := curlDataValue(env, part)
		if err != nil {
			return nil, err
		}
		body.Write(payload)
	}
	return body.Bytes(), nil
}

func curlDataValue(env *commandEnv, part curlDataPart) ([]byte, error) {
	if !strings.HasPrefix(part.value, "@") {
		return []byte(part.value), nil
	}
	source := strings.TrimPrefix(part.value, "@")
	if source == "-" {
		data, err := io.ReadAll(env.stdin())
		if err != nil {
			return nil, err
		}
		return curlNormalizeDataBytes(part.mode, data), nil
	}
	resolvedPath, err := env.resolvePathArg(source)
	if err != nil {
		return nil, err
	}
	file, err := env.hc.FileSystem.Open(resolvedPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	return curlNormalizeDataBytes(part.mode, data), nil
}

func curlNormalizeDataBytes(mode curlDataMode, data []byte) []byte {
	if mode != curlDataModeForm {
		return data
	}
	if bytes.IndexByte(data, '\r') < 0 && bytes.IndexByte(data, '\n') < 0 && bytes.IndexByte(data, 0) < 0 {
		return data
	}
	normalized := make([]byte, 0, len(data))
	for _, b := range data {
		switch b {
		case '\r', '\n', 0:
			continue
		default:
			normalized = append(normalized, b)
		}
	}
	return normalized
}

func curlOutputWriter(env *commandEnv, outputPath string) (io.Writer, io.Closer, error) {
	if outputPath == "" || outputPath == "-" {
		return env.stdout(), nil, nil
	}
	resolvedPath, err := env.resolvePathArg(outputPath)
	if err != nil {
		return nil, nil, err
	}
	file, err := env.hc.FileSystem.OpenFile(resolvedPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o666)
	if err != nil {
		return nil, nil, err
	}
	return file, file, nil
}

func writeCurlHeaders(writer io.Writer, response *http.Response) error {
	if _, err := fmt.Fprintf(writer, "%s %s\r\n", response.Proto, response.Status); err != nil {
		return err
	}
	headerNames := make([]string, 0, len(response.Header))
	for name := range response.Header {
		headerNames = append(headerNames, name)
	}
	sort.Strings(headerNames)
	for _, name := range headerNames {
		for _, value := range response.Header.Values(name) {
			if _, err := fmt.Fprintf(writer, "%s: %s\r\n", name, value); err != nil {
				return err
			}
		}
	}
	_, err := io.WriteString(writer, "\r\n")
	return err
}

func secondsDuration(seconds float64) time.Duration {
	return time.Duration(seconds * float64(time.Second))
}

const (
	defaultCurlMaxRedirects      = 50
	curlExitHTTPPageNotRetrieved = interp.ExitStatus(22)
	curlExitTooManyRedirects     = interp.ExitStatus(47)
)

type curlDataMode int

const (
	curlDataModeForm curlDataMode = iota
	curlDataModeBinary
)

type curlDataPart struct {
	mode  curlDataMode
	value string
}

type curlRedirectOptions struct {
	followLocation  bool
	locationTrusted bool
	post301         bool
	post302         bool
	post303         bool
	maxRedirects    int
	explicitMethod  bool
	initialURL      *url.URL
}

type curlTransferResult struct {
	responseCode    int
	effectiveURL    string
	contentType     string
	redirectCount   int
	uploadedBytes   int64
	downloadedBytes int64
	totalDuration   time.Duration
	exitCode        int
	errorMessage    string
}

type curlStatusError struct {
	message string
	status  interp.ExitStatus
}

func (err *curlStatusError) Error() string {
	return err.message
}

func (err *curlStatusError) Unwrap() error {
	return err.status
}

func newCurlStatusError(status interp.ExitStatus, format string, args ...any) error {
	return &curlStatusError{
		message: fmt.Sprintf(format, args...),
		status:  status,
	}
}

func newCurlTransferResult(
	response *http.Response,
	currentURL *url.URL,
	redirectCount int,
	uploadedBytes, downloadedBytes int64,
	startedAt time.Time,
	transferErr error,
) curlTransferResult {
	result := curlTransferResult{
		redirectCount:   redirectCount,
		uploadedBytes:   uploadedBytes,
		downloadedBytes: downloadedBytes,
		totalDuration:   time.Since(startedAt),
		exitCode:        curlExitCode(transferErr),
		errorMessage:    curlErrorMessage(transferErr),
	}
	if response != nil {
		result.responseCode = response.StatusCode
		result.contentType = response.Header.Get("Content-Type")
		if response.Request != nil && response.Request.URL != nil {
			result.effectiveURL = response.Request.URL.String()
		}
	}
	if result.effectiveURL == "" && currentURL != nil {
		result.effectiveURL = currentURL.String()
	}
	return result
}

func curlPrepareRedirectRequest(
	ctx context.Context,
	request *http.Request,
	response *http.Response,
	currentBody []byte,
	baseHeaders http.Header,
	opts curlRedirectOptions,
	redirectsFollowed int,
) (*http.Request, []byte, bool, error) {
	if !opts.followLocation {
		return nil, nil, false, nil
	}
	nextMethod, nextBody, shouldRedirect := curlRedirectMethodAndBody(request.Method, currentBody, response.StatusCode, opts)
	if !shouldRedirect {
		return nil, nil, false, nil
	}

	locationValue := strings.TrimSpace(response.Header.Get("Location"))
	if locationValue == "" {
		return nil, nil, false, nil
	}
	if opts.maxRedirects >= 0 && redirectsFollowed >= opts.maxRedirects {
		return nil, nil, false, newCurlStatusError(curlExitTooManyRedirects, "curl: maximum (%d) redirects followed", opts.maxRedirects)
	}

	locationURL, err := url.Parse(locationValue)
	if err != nil {
		return nil, nil, false, fmt.Errorf("curl: failed to parse Location header %q: %w", locationValue, err)
	}
	nextURL, err := request.URL.Parse(locationValue)
	if err != nil {
		return nil, nil, false, fmt.Errorf("curl: failed to parse Location header %q: %w", locationValue, err)
	}

	nextRequest, err := newCurlRequestWithBody(ctx, nextMethod, nextURL.String(), nextBody)
	if err != nil {
		return nil, nil, false, err
	}
	nextRequest.Header = baseHeaders.Clone()
	if !opts.locationTrusted && !curlSameOrigin(opts.initialURL, nextURL) {
		nextRequest.Header.Del("Authorization")
		nextRequest.Header.Del("Cookie")
	}
	if request.Host != "" && request.Host != request.URL.Host && !locationURL.IsAbs() {
		nextRequest.Host = request.Host
	}
	return nextRequest, nextBody, true, nil
}

func curlRedirectMethodAndBody(method string, body []byte, statusCode int, opts curlRedirectOptions) (string, []byte, bool) {
	switch statusCode {
	case http.StatusMovedPermanently:
		if opts.explicitMethod {
			return method, body, true
		}
		if method == http.MethodPost {
			if opts.post301 {
				return method, body, true
			}
			return http.MethodGet, nil, true
		}
		return method, body, true
	case http.StatusFound:
		if opts.explicitMethod {
			return method, body, true
		}
		if method == http.MethodPost {
			if opts.post302 {
				return method, body, true
			}
			return http.MethodGet, nil, true
		}
		return method, body, true
	case http.StatusSeeOther:
		if opts.explicitMethod {
			return method, body, true
		}
		if method == http.MethodPost && !opts.post303 {
			return http.MethodGet, nil, true
		}
		return method, body, true
	case http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return method, body, true
	default:
		return "", nil, false
	}
}

func curlSameOrigin(left, right *url.URL) bool {
	if left == nil || right == nil {
		return false
	}
	if !strings.EqualFold(left.Scheme, right.Scheme) {
		return false
	}
	if !strings.EqualFold(left.Hostname(), right.Hostname()) {
		return false
	}
	return curlCanonicalPort(left) == curlCanonicalPort(right)
}

func curlCanonicalPort(u *url.URL) string {
	port := u.Port()
	if port != "" {
		return port
	}
	switch strings.ToLower(u.Scheme) {
	case "http":
		return "80"
	case "https":
		return "443"
	default:
		return ""
	}
}

func newCurlRequestWithBody(ctx context.Context, method, rawURL string, body []byte) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	return http.NewRequestWithContext(ctx, method, rawURL, reader)
}

func closeCurlRedirectResponse(response *http.Response) {
	if response == nil || response.Body == nil {
		return
	}
	const maxBodySlurpSize = 2 << 10
	if response.ContentLength == -1 || response.ContentLength <= maxBodySlurpSize {
		io.CopyN(io.Discard, response.Body, maxBodySlurpSize)
	}
	response.Body.Close()
}

func curlMaybeShowError(env *commandEnv, silent, showError bool, err error) error {
	if err != nil && (!silent || showError) {
		_, _ = fmt.Fprintln(env.stderr(), err.Error())
	}
	return err
}

func writeCurlWriteOut(writer io.Writer, result curlTransferResult, format string) error {
	if format == "" {
		return nil
	}
	rendered, err := renderCurlWriteOut(result, format)
	if err != nil {
		return err
	}
	_, err = io.WriteString(writer, rendered)
	return err
}

func renderCurlWriteOut(result curlTransferResult, format string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(format); i++ {
		switch format[i] {
		case '\\':
			if i+1 >= len(format) {
				b.WriteByte('\\')
				continue
			}
			i++
			switch format[i] {
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case '\\':
				b.WriteByte('\\')
			default:
				b.WriteByte('\\')
				b.WriteByte(format[i])
			}
		case '%':
			if i+1 >= len(format) {
				b.WriteByte('%')
				continue
			}
			if format[i+1] == '%' {
				b.WriteByte('%')
				i++
				continue
			}
			if format[i+1] != '{' {
				b.WriteByte('%')
				continue
			}
			end := strings.IndexByte(format[i+2:], '}')
			if end < 0 {
				return "", fmt.Errorf("curl: invalid write-out format %q", format)
			}
			variable := format[i+2 : i+2+end]
			value, err := curlWriteOutValue(result, variable)
			if err != nil {
				return "", err
			}
			b.WriteString(value)
			i += end + 2
		default:
			b.WriteByte(format[i])
		}
	}
	return b.String(), nil
}

func curlWriteOutValue(result curlTransferResult, variable string) (string, error) {
	switch variable {
	case "http_code", "response_code":
		return fmt.Sprintf("%03d", result.responseCode), nil
	case "url_effective":
		return result.effectiveURL, nil
	case "content_type":
		return result.contentType, nil
	case "num_redirects":
		return strconv.Itoa(result.redirectCount), nil
	case "exitcode":
		return strconv.Itoa(result.exitCode), nil
	case "errormsg":
		return result.errorMessage, nil
	default:
		return "", fmt.Errorf("curl: unsupported write-out variable %%{%s}", variable)
	}
}

func curlExitCode(err error) int {
	if err == nil {
		return 0
	}
	var status interp.ExitStatus
	if errors.As(err, &status) {
		return int(status)
	}
	return 1
}

func curlErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
