package githubcommon

import "testing"

func TestNormalizeRepoPath(t *testing.T) {
	t.Parallel()

	got, err := NormalizeRepoPath(`\services/payments//flaky_test.go`)
	if err != nil {
		t.Fatalf("NormalizeRepoPath() error = %v", err)
	}
	if got != "services/payments/flaky_test.go" {
		t.Fatalf("NormalizeRepoPath() = %q, want %q", got, "services/payments/flaky_test.go")
	}
}

func TestNormalizeRepoPathRejectsTraversal(t *testing.T) {
	t.Parallel()

	if _, err := NormalizeRepoPath("../secrets.txt"); err == nil {
		t.Fatal("NormalizeRepoPath() error = nil, want traversal rejection")
	}
}

func TestPageInfoFromLinkHeaderTracksNextLinkWithoutPageNumber(t *testing.T) {
	t.Parallel()

	pageInfo := PageInfoFromLinkHeader(Pagination{Page: 1, PerPage: 20}, `<https://api.github.com/repos/acme/monorepo/actions/runs?after=cursor>; rel="next"`)
	if pageInfo == nil {
		t.Fatal("PageInfoFromLinkHeader() = nil, want page info")
	}
	if !pageInfo.HasNextPage {
		t.Fatal("pageInfo.HasNextPage = false, want true")
	}
	if pageInfo.NextPage != nil {
		t.Fatalf("pageInfo.NextPage = %#v, want nil when next page number is unavailable", pageInfo.NextPage)
	}
}
