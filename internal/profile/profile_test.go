package profile

import (
	"strings"
	"testing"

	"github.com/scarletmu/openwhisker/internal/policy"
)

func TestBuildBundleCompilesRawOrganizerSkillFromProfile(t *testing.T) {
	bundle := BuildBundle(policy.KnowledgeVaultConventions())
	if bundle.Profile.ID != "knowledge-vault" {
		t.Fatalf("profile id = %q", bundle.Profile.ID)
	}
	if bundle.Profile.Source != ProfileSourceManualConfig {
		t.Fatalf("profile source = %q", bundle.Profile.Source)
	}
	if len(bundle.Skills) != 1 {
		t.Fatalf("skill count = %d, want 1", len(bundle.Skills))
	}
	skill := bundle.Skills[0]
	if skill.Path != RawOrganizerSkillPath {
		t.Fatalf("skill path = %q", skill.Path)
	}
	for _, want := range []string{
		"compiled from the current VaultProfile",
		"Knowledge/Drafts",
		"status/needs-review",
	} {
		if !strings.Contains(skill.Content, want) {
			t.Fatalf("skill content missing %q:\n%s", want, skill.Content)
		}
	}
}

func TestContextDocumentsExposeSkillBeforeProfile(t *testing.T) {
	docs := ContextDocuments(policy.DefaultConventions())
	if len(docs) != 2 {
		t.Fatalf("doc count = %d, want 2", len(docs))
	}
	if docs[0].Path != RawOrganizerSkillPath {
		t.Fatalf("first doc path = %q, want skill", docs[0].Path)
	}
	if docs[1].Path != ProfileDocumentPath {
		t.Fatalf("second doc path = %q, want profile", docs[1].Path)
	}
	if !strings.Contains(docs[1].Content, "Profile source: manual-config") {
		t.Fatalf("profile markdown = %s", docs[1].Content)
	}
}
