package duo

import "testing"

func TestParseRemoteDigest(t *testing.T) {
	const good = "sha256:9cc9e9ee6cd5aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bare digest from --format", good + "\n", good},
		{"empty output", "", ""},
		// Some docker/buildx versions ignore --format and print the human-readable block;
		// the digest must be extracted from the Digest: line, not the Name: line.
		{"default human block", "Name:      ghcr.io/foo/bar:latest\nMediaType: application/vnd.oci.image.index.v1+json\nDigest:    " + good + "\n", good},
		{"name line only, no digest", "Name:      ghcr.io/foo/bar:latest\n", ""},
		{"malformed digest rejected", "Name: g\n", ""},
		{"truncated hex rejected", "sha256:abc123\n", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseRemoteDigest(c.in); got != c.want {
				t.Errorf("parseRemoteDigest(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
