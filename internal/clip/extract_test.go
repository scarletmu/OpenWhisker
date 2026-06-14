package clip

import (
	"strings"
	"testing"
)

func TestExtractArticleHappyPath(t *testing.T) {
	doc := `<!doctype html>
<html>
<head>
  <title>How Raft Works &mdash; Notes</title>
  <style>.x{color:red}</style>
  <script>console.log("tracking")</script>
</head>
<body>
  <nav><a href="/">Home</a><a href="/about">About</a></nav>
  <header>site chrome</header>
  <article>
    <h1>How Raft Works</h1>
    <p>Raft is a <strong>consensus</strong> algorithm. See the <a href="https://raft.example/paper">paper</a>.</p>
    <h2>Leader election</h2>
    <ul>
      <li>Followers</li>
      <li>Candidates</li>
    </ul>
    <pre><code>term := currentTerm + 1</code></pre>
  </article>
  <footer>copyright tracking pixels</footer>
</body>
</html>`

	got := Extract(doc)
	if got.Title != "How Raft Works — Notes" {
		t.Fatalf("title = %q", got.Title)
	}
	body := got.Body
	for _, want := range []string{
		"# How Raft Works",
		"**consensus**",
		"[paper](https://raft.example/paper)",
		"## Leader election",
		"- Followers",
		"- Candidates",
		"term := currentTerm + 1",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
	// Boilerplate must be gone.
	for _, banned := range []string{"tracking", "site chrome", "copyright", "About", "color:red"} {
		if strings.Contains(body, banned) {
			t.Fatalf("body leaked boilerplate %q:\n%s", banned, body)
		}
	}
}

func TestExtractFallsBackToBodyWithoutArticle(t *testing.T) {
	doc := `<html><head><title>Plain</title></head><body>
	<script>x()</script>
	<p>Just a paragraph with a <em>word</em>.</p>
	</body></html>`
	got := Extract(doc)
	if got.Title != "Plain" {
		t.Fatalf("title = %q", got.Title)
	}
	if !strings.Contains(got.Body, "Just a paragraph with a *word*.") {
		t.Fatalf("body = %q", got.Body)
	}
	if strings.Contains(got.Body, "x()") {
		t.Fatalf("script leaked: %q", got.Body)
	}
}

func TestExtractCollapsesBlankLines(t *testing.T) {
	doc := `<body><p>one</p><p></p><p></p><p>two</p></body>`
	got := Extract(doc)
	if strings.Contains(got.Body, "\n\n\n") {
		t.Fatalf("body has 3+ consecutive newlines:\n%q", got.Body)
	}
	if !strings.Contains(got.Body, "one") || !strings.Contains(got.Body, "two") {
		t.Fatalf("body = %q", got.Body)
	}
}
