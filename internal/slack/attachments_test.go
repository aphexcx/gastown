package slack

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAttachmentAction(t *testing.T) {
	cases := []struct {
		name     string
		file     AttachmentMeta
		wantKind AttachmentAction
	}{
		{"tiny image", AttachmentMeta{ID: "F1", Name: "a.png", MimeType: "image/png", Size: 1024}, AttachmentActionDownload},
		{"5mb image", AttachmentMeta{ID: "F2", Name: "b.png", MimeType: "image/png", Size: 5 * 1024 * 1024}, AttachmentActionDownload},
		{"just over 5mb", AttachmentMeta{ID: "F3", Name: "c.zip", Size: 5*1024*1024 + 1}, AttachmentActionMetadataOnly},
		{"100mb", AttachmentMeta{ID: "F4", Name: "d.mp4", Size: 100 * 1024 * 1024}, AttachmentActionMetadataOnly},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.wantKind, DecideAttachment(tc.file))
		})
	}
}

func TestSanitizeAttachmentFilename(t *testing.T) {
	cases := map[string]string{
		"photo.png":              "photo.png",
		"../../etc/passwd":       "etc_passwd",
		"my file.png":            "my_file.png",
		"weird@name$.jpg":        "weird_name_.jpg",
		"":                       "attachment",
		"....":                   "attachment",
		"a/b/c.txt":              "a_b_c.txt",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			require.Equal(t, want, SanitizeAttachmentFilename(in))
		})
	}
}
