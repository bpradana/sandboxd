package buildinfo

import "testing"

func TestFormatVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		environment string
		tag         string
		commit      string
		local       bool
		want        string
	}{
		{
			name:        "production uses release tag",
			environment: "production",
			tag:         "1.8.0",
			commit:      "3fa41bc",
			want:        "1.8.0",
		},
		{
			name:        "staging uses commit",
			environment: "aws-staging",
			commit:      "3fa41bc",
			want:        "staging.3fa41bc",
		},
		{
			name:        "develop uses abbreviated prefix",
			environment: "develop-aws",
			commit:      "3fa41bc",
			want:        "dev.3fa41bc",
		},
		{
			name:        "local develop is identified",
			environment: "development",
			commit:      "3fa41bc",
			local:       true,
			want:        "dev.local+3fa41bc",
		},
		{
			name:        "environment matching is case insensitive",
			environment: "Acme-STAGING-AWS",
			commit:      "3fa41bc",
			want:        "staging.3fa41bc",
		},
		{
			name:        "unknown environment is not assigned a release",
			environment: "test",
			tag:         "1.8.0",
			commit:      "3fa41bc",
			want:        "unknown",
		},
		{
			name:        "missing production tag is explicit",
			environment: "production",
			want:        "unknown",
		},
		{
			name:        "missing non-production metadata is explicit",
			environment: "staging",
			want:        "staging.unknown",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := formatVersion(tt.environment, tt.tag, tt.commit, tt.local)
			if got != tt.want {
				t.Fatalf("formatVersion() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestShortCommit(t *testing.T) {
	t.Parallel()

	if got := shortCommit("3fa41bc9a7d1"); got != "3fa41bc" {
		t.Fatalf("shortCommit() = %q, want %q", got, "3fa41bc")
	}
	if got := shortCommit(""); got != "unknown" {
		t.Fatalf("shortCommit() = %q, want %q", got, "unknown")
	}
}
