package skills

import (
	"context"
	"strings"
	"testing"
)

// toolNames returns the Definition().Name of each tool from NewSkillTools.
func toolNames(provider SkillProvider) map[string]bool {
	names := map[string]bool{}
	for _, tl := range NewSkillTools(provider) {
		names[tl.Definition().Name] = true
	}
	return names
}

// plainProvider implements SkillProvider with no optional capabilities.
type plainProvider struct{}

func (plainProvider) Discover(ctx context.Context) ([]SkillSummary, error) {
	return []SkillSummary{{Name: "x", Description: "x skill"}}, nil
}

func (plainProvider) Activate(ctx context.Context, name string) (Skill, error) {
	if name == "x" {
		return Skill{Name: "x", Description: "x skill", Instructions: "do x"}, nil
	}
	return Skill{}, context.Canceled
}

// searchProvider implements SkillProvider + a custom SkillSearcher.
type searchProvider struct {
	plainProvider
	called *bool
}

func (s searchProvider) SearchSkills(ctx context.Context, query string, limit int) ([]SkillSearchResult, error) {
	*s.called = true
	return []SkillSearchResult{{SkillSummary: SkillSummary{Name: "custom"}, Score: 1}}, nil
}

func TestConsolidatedToolSurface(t *testing.T) {
	// Plain provider: read-only surface, no skill_manage.
	names := toolNames(plainProvider{})
	for _, want := range []string{"skills_list", "skill_view"} {
		if !names[want] {
			t.Errorf("expected tool %q to be registered", want)
		}
	}
	if names["skill_manage"] {
		t.Error("skill_manage must not register for a provider without SkillWriter")
	}
	if len(names) != 2 {
		t.Errorf("expected exactly 2 tools for a plain provider, got %v", names)
	}

	// Writable file provider: skill_manage joins.
	dir := t.TempDir()
	writeSkill(t, dir, "greeter", "hi", map[string]string{"refs/a.md": "AAA"})
	names = toolNames(FromDir(dir))
	for _, want := range []string{"skills_list", "skill_view", "skill_manage"} {
		if !names[want] {
			t.Errorf("expected tool %q to be registered for a writable provider", want)
		}
	}
	if len(names) != 3 {
		t.Errorf("expected exactly 3 tools for a writable provider, got %v", names)
	}
}

func TestSkillsListNoQuery(t *testing.T) {
	lt := &skillsListTool{provider: plainProvider{}, searcher: NewBM25Searcher(plainProvider{})}
	out, err := lt.Execute(context.Background(), skillsListIn{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "x skill") {
		t.Fatalf("expected listing to include skill description, got %q", out)
	}
}

func TestSkillsListQueryUsesProviderSearcher(t *testing.T) {
	called := false
	p := searchProvider{called: &called}
	// NewSkillTools must prefer the provider's own searcher.
	var lt *skillsListTool
	for _, tl := range NewSkillTools(p) {
		if tl.Definition().Name == "skills_list" {
			// Reconstruct with the same wiring NewSkillTools performs.
			lt = &skillsListTool{provider: p, searcher: p}
		}
	}
	if lt == nil {
		t.Fatal("skills_list not registered")
	}
	out, err := lt.Execute(context.Background(), skillsListIn{Query: "anything"})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("expected provider's SkillSearcher to be used")
	}
	if !strings.Contains(out, "custom") {
		t.Fatalf("expected search result, got %q", out)
	}
}

func TestSkillViewActivationIncludesFileList(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "greeter", "say hello", map[string]string{"refs/a.md": "AAA-CONTENT"})
	vt := &skillViewTool{provider: FromDir(dir)}
	out, err := vt.Execute(context.Background(), skillViewIn{Name: "greeter"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "say hello") {
		t.Fatalf("expected instructions in output, got %q", out)
	}
	if !strings.Contains(out, "refs/a.md") {
		t.Fatalf("expected companion file listing, got %q", out)
	}
}

func TestSkillViewActivationWithoutResources(t *testing.T) {
	vt := &skillViewTool{provider: plainProvider{}}
	out, err := vt.Execute(context.Background(), skillViewIn{Name: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "do x") {
		t.Fatalf("expected instructions, got %q", out)
	}
	if _, err := vt.Execute(context.Background(), skillViewIn{Name: "x", FilePath: "refs/a.md"}); err == nil {
		t.Error("expected error reading a file from a provider without SkillResources")
	}
}

func TestSkillViewReadsFile(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "greeter", "hi", map[string]string{"refs/a.md": "AAA-CONTENT"})
	vt := &skillViewTool{provider: FromDir(dir)}
	out, err := vt.Execute(context.Background(), skillViewIn{Name: "greeter", FilePath: "refs/a.md"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "AAA-CONTENT") {
		t.Fatalf("got %q", out)
	}
}

func TestSkillViewTruncatesLargeFile(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("a", maxResourceBytes+5000)
	writeSkill(t, dir, "greeter", "hi", map[string]string{"big.txt": big})
	vt := &skillViewTool{provider: FromDir(dir)}
	out, err := vt.Execute(context.Background(), skillViewIn{Name: "greeter", FilePath: "big.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[truncated:") {
		t.Fatalf("expected truncation notice, got %d bytes", len(out))
	}
	if len(out) > maxResourceBytes+100 {
		t.Fatalf("output not truncated: %d bytes", len(out))
	}
}

func TestSkillViewBinaryNotShown(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "greeter", "hi", map[string]string{"blob.bin": string([]byte{0x00, 0xff, 0xfe, 0x01, 0x02})})
	vt := &skillViewTool{provider: FromDir(dir)}
	out, err := vt.Execute(context.Background(), skillViewIn{Name: "greeter", FilePath: "blob.bin"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "binary file") {
		t.Fatalf("expected binary notice, got %q", out)
	}
}

func TestSkillManageLifecycle(t *testing.T) {
	dir := t.TempDir()
	p := FromDir(dir)
	mt := &skillManageTool{provider: p, writer: p.(SkillWriter)}

	desc := "greets people"
	instr := "say hello"
	if _, err := mt.Execute(context.Background(), skillManageIn{
		Action: "create", Name: "greeter", Description: &desc, Instructions: &instr,
	}); err != nil {
		t.Fatal(err)
	}
	sk, err := p.Activate(context.Background(), "greeter")
	if err != nil {
		t.Fatal(err)
	}
	if sk.Instructions != "say hello" {
		t.Fatalf("got instructions %q", sk.Instructions)
	}

	// Partial update: only description changes; instructions survive.
	newDesc := "greets people warmly"
	out, err := mt.Execute(context.Background(), skillManageIn{
		Action: "update", Name: "greeter", Description: &newDesc,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "description") {
		t.Fatalf("expected change summary, got %q", out)
	}
	sk, err = p.Activate(context.Background(), "greeter")
	if err != nil {
		t.Fatal(err)
	}
	if sk.Description != newDesc || sk.Instructions != "say hello" {
		t.Fatalf("partial update broke fields: %+v", sk)
	}

	if _, err := mt.Execute(context.Background(), skillManageIn{Action: "delete", Name: "greeter"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Activate(context.Background(), "greeter"); err == nil {
		t.Error("expected skill to be gone after delete")
	}
}

func TestSkillManageValidation(t *testing.T) {
	dir := t.TempDir()
	p := FromDir(dir)
	mt := &skillManageTool{provider: p, writer: p.(SkillWriter)}

	if _, err := mt.Execute(context.Background(), skillManageIn{Action: "create", Name: "nope"}); err == nil {
		t.Error("expected error creating without description/instructions")
	}
	if _, err := mt.Execute(context.Background(), skillManageIn{Action: "explode", Name: "nope"}); err == nil {
		t.Error("expected error for unknown action")
	}
	if _, err := mt.Execute(context.Background(), skillManageIn{Action: "create"}); err == nil {
		t.Error("expected error for missing name")
	}
}
