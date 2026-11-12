package agent

import (
	"strings"
	"testing"
	"time"
)

func TestParseEHGalleryRefFromURL(t *testing.T) {
	ref, err := parseEHGalleryRef("https://exhentai.org/g/4022117/22c0b08fdc/", "", "", "")
	if err != nil {
		t.Fatalf("parseEHGalleryRef error: %v", err)
	}
	if ref.Site != "EX" || ref.GID != "4022117" || ref.Token != "22c0b08fdc" {
		t.Fatalf("unexpected ref: %+v", ref)
	}
	if ref.URL != "https://exhentai.org/g/4022117/22c0b08fdc/" {
		t.Fatalf("unexpected url: %s", ref.URL)
	}
}

func TestParseEHGalleryRefDetectsEHSite(t *testing.T) {
	ref, err := parseEHGalleryRef("e-hentai.org/g/123/abcdef/", "", "", "")
	if err != nil {
		t.Fatalf("parseEHGalleryRef error: %v", err)
	}
	if ref.Site != "EH" || ref.URL != "https://e-hentai.org/g/123/abcdef/" {
		t.Fatalf("unexpected ref: %+v", ref)
	}
}

func TestParseEHGalleryRefRejectsInvalidLink(t *testing.T) {
	if _, err := parseEHGalleryRef("https://exhentai.org/tag/language", "", "", ""); err == nil {
		t.Fatal("expected error for non-gallery link")
	}
	if _, err := parseEHGalleryRef("https://example.com/g/1/token/", "", "", ""); err == nil {
		t.Fatal("expected error for unknown site")
	}
}

func TestSelectEHGalleryThumbsRangeAndLimit(t *testing.T) {
	thumbs := []ehGalleryThumb{
		{PageNo: 1}, {PageNo: 2}, {PageNo: 3}, {PageNo: 4}, {PageNo: 5},
	}

	got := selectEHGalleryThumbs(thumbs, 2, 4, 0, 0)
	if len(got) != 3 || got[0].PageNo != 2 || got[2].PageNo != 4 {
		t.Fatalf("unexpected range selection: %+v", got)
	}

	got = selectEHGalleryThumbs(thumbs, 0, 0, 2, 0)
	if len(got) != 2 || got[0].PageNo != 1 || got[1].PageNo != 2 {
		t.Fatalf("unexpected limited selection: %+v", got)
	}
}

func TestSelectEHGalleryThumbsFirstN(t *testing.T) {
	thumbs := []ehGalleryThumb{
		{PageNo: 1}, {PageNo: 2}, {PageNo: 3}, {PageNo: 4}, {PageNo: 5},
	}

	got := selectEHGalleryThumbs(thumbs, 0, 0, 0, 3)
	if len(got) != 3 || got[0].PageNo != 1 || got[2].PageNo != 3 {
		t.Fatalf("unexpected first_n selection: %+v", got)
	}

	// first_n=0 表示全本发送。
	got = selectEHGalleryThumbs(thumbs, 0, 0, 0, 0)
	if len(got) != len(thumbs) {
		t.Fatalf("first_n=0 should keep all thumbs, got %d", len(got))
	}

	// first_n 大于总页数时按实际页数返回。
	got = selectEHGalleryThumbs(thumbs, 0, 0, 0, 99)
	if len(got) != len(thumbs) {
		t.Fatalf("first_n beyond page count should keep all thumbs, got %d", len(got))
	}

	// first_n 与 start_page 同时使用时取交集。
	got = selectEHGalleryThumbs(thumbs, 2, 0, 0, 3)
	if len(got) != 2 || got[0].PageNo != 2 || got[1].PageNo != 3 {
		t.Fatalf("unexpected first_n with start_page selection: %+v", got)
	}
}

func TestParseEHImagePagePrefersMainImageTag(t *testing.T) {
	body := `<div><img src="https://thumb.example/x.jpg"></div>` +
		`<img id="img" src="https://img.example/full.jpg" />` +
		`<a href="https://img.example/orig.png">Download original</a>`

	info := parseEHImagePage(body)
	if info.ImageURL != "https://img.example/full.jpg" {
		t.Fatalf("unexpected image url: %s", info.ImageURL)
	}
	if info.OriginalURL != "https://img.example/orig.png" {
		t.Fatalf("unexpected original url: %s", info.OriginalURL)
	}
}

func TestParseEHImagePageUnescapesEntities(t *testing.T) {
	info := parseEHImagePage(`<img id="img" src="https://img.example/a.jpg?x=1&amp;y=2">`)
	if info.ImageURL != "https://img.example/a.jpg?x=1&y=2" {
		t.Fatalf("unexpected image url: %s", info.ImageURL)
	}
}

func TestParseEHGalleryThumbsKeepsPageOrder(t *testing.T) {
	body := `<a href="https://e-hentai.org/s/aaa/1-2">2</a>` +
		`<a href="/s/bbb/1-1">1</a>`

	thumbs := parseEHGalleryThumbs(body, "EH")
	if len(thumbs) != 2 {
		t.Fatalf("unexpected thumb count: %d", len(thumbs))
	}
	if thumbs[0].PageNo != 2 || thumbs[0].Hash != "aaa" {
		t.Fatalf("unexpected thumb: %+v", thumbs[0])
	}
	if thumbs[1].PageNo != 1 {
		t.Fatalf("unexpected thumb: %+v", thumbs[1])
	}
	if thumbs[1].URL != "https://e-hentai.org/s/bbb/1-1" {
		t.Fatalf("relative href should be resolved to the site base url: %s", thumbs[1].URL)
	}
}

func TestFinalizeEHGalleryResultReportsSkippedPages(t *testing.T) {
	p := &plugin{}
	result := ehGallerySendResult{
		RequestedPages: 5,
		ResolvedImages: 3,
		BatchTotal:     1,
		BatchSent:      1,
		SentImages:     3,
		Failures: []ehGalleryImageFailure{
			{Page: 5, Error: "x"},
			{Page: 4, Error: ehImageQuotaPendingError},
		},
	}

	p.finalizeEHGalleryResult(&result, time.Now(), 12)

	if result.SkippedPages != 2 {
		t.Fatalf("unexpected skipped pages: %d", result.SkippedPages)
	}
	if !result.OK {
		t.Fatal("expected ok for fully sent batches")
	}
	if result.Complete {
		t.Fatal("complete must be false when pages were skipped")
	}
	if result.RequestCount != 12 {
		t.Fatalf("unexpected request count: %d", result.RequestCount)
	}
	if len(result.Failures) != 2 || result.Failures[0].Page != 4 {
		t.Fatalf("failures should be sorted by page: %+v", result.Failures)
	}
}

func TestFinalizeEHGalleryResultTruncatesFailures(t *testing.T) {
	p := &plugin{}
	result := ehGallerySendResult{RequestedPages: ehGalleryMaxReportedFails + 5}
	for i := 1; i <= ehGalleryMaxReportedFails+5; i++ {
		result.Failures = append(result.Failures, ehGalleryImageFailure{Page: i, Error: "x"})
	}

	p.finalizeEHGalleryResult(&result, time.Now(), 0)

	if len(result.Failures) != ehGalleryMaxReportedFails {
		t.Fatalf("unexpected failure count: %d", len(result.Failures))
	}
	if result.FailuresOmitted != 5 {
		t.Fatalf("unexpected omitted count: %d", result.FailuresOmitted)
	}
}

func TestFinalizeEHGalleryResultWarnsFirstN(t *testing.T) {
	p := &plugin{}
	result := ehGallerySendResult{
		PageCount:      100,
		FirstN:         10,
		RequestedPages: 10,
		ResolvedImages: 10,
		BatchTotal:     1,
		BatchSent:      1,
		SentImages:     10,
	}

	p.finalizeEHGalleryResult(&result, time.Now(), 0)

	found := false
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "first_n=10") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected first_n warning, got %+v", result.Warnings)
	}
}

func TestSplitEHGalleryImagesIntoBatches(t *testing.T) {
	images := make([]ehLocalGalleryImage, 0, 65)
	for i := 1; i <= 65; i++ {
		images = append(images, ehLocalGalleryImage{PageNo: i, FileURL: "file://x"})
	}

	batches := make([][]ehLocalGalleryImage, 0, 3)
	for start := 0; start < len(images); start += ehGalleryDefaultBatchSize {
		end := start + ehGalleryDefaultBatchSize
		if end > len(images) {
			end = len(images)
		}
		batches = append(batches, images[start:end])
	}

	if len(batches) != 3 {
		t.Fatalf("unexpected batch count: %d", len(batches))
	}
	if len(batches[0]) != 30 || len(batches[1]) != 30 || len(batches[2]) != 5 {
		t.Fatalf("unexpected batch sizes: %d/%d/%d", len(batches[0]), len(batches[1]), len(batches[2]))
	}
	if batches[2][4].PageNo != 65 {
		t.Fatalf("unexpected last page: %d", batches[2][4].PageNo)
	}
}
