package structpages

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"
)

// App patterns the generator (tools/gen) might not cover, each run the three
// ways (reflect, generated, tinygo) by the ordinary suite: whatever reflect
// does is what the others must do. A pattern the generated code can't do
// yet is marked with skipInTablesBuild and a "generator gap:" reason, so
// mise run parity lists it.

// A request-arg provider whose type is a named function type.
type gpUserFunc func(*http.Request) (*raUser, error)

var gpNamedProvider gpUserFunc = raCurrentUser

func TestGeneratorGap_namedFuncTypeProvider(t *testing.T) {
	mux := http.NewServeMux()
	if _, err := Mount(mux, raRoot{}, "/", "App", WithRequestArgs(gpNamedProvider)); err != nil {
		t.Fatal(err)
	}
	if _, body := serve(t, mux, raGet("/account", "ana")); body != "ana/ana" {
		t.Errorf("rendered %q, want ana/ana", body)
	}
}

// A request-arg provider that is a method value of a service.
type gpAuth struct{ prefix string }

func (a gpAuth) user(r *http.Request) (*raUser, error) {
	return &raUser{name: a.prefix + r.Header.Get("X-User")}, nil
}

func TestGeneratorGap_methodValueProvider(t *testing.T) {
	mux := http.NewServeMux()
	if _, err := Mount(mux, raRoot{}, "/", "App", WithRequestArgs(gpAuth{"x-"}.user)); err != nil {
		t.Fatal(err)
	}
	if _, body := serve(t, mux, raGet("/account", "ana")); body != "x-ana/x-ana" {
		t.Errorf("rendered %q, want x-ana/x-ana", body)
	}
}

// A page's RequestArgs returning a function literal.
type gpArgsPage struct{}

func (gpArgsPage) RequestArgs() []any {
	return []any{func(r *http.Request) (raTenant, error) { return raTenant(r.Header.Get("X-Tenant")), nil }}
}
func (gpArgsPage) Page(t raTenant) component { return testComponent{string(t)} }

type gpArgsRoot struct {
	Tenant gpArgsPage `route:"/tenant Tenant"`
}

func TestGeneratorGap_requestArgsLiteral(t *testing.T) {
	mux := http.NewServeMux()
	if _, err := Mount(mux, gpArgsRoot{}, "/", "App"); err != nil {
		t.Fatal(err)
	}
	req := raGet("/tenant", "")
	req.Header.Set("X-Tenant", "acme")
	if _, body := serve(t, mux, req); body != "acme" {
		t.Errorf("rendered %q, want acme", body)
	}
}

// A route field whose type is an alias of a page, and one that is a pointer
// to a page.
type gpAliasPage = raAccountPage

type gpAliasRoot struct {
	Alias   gpAliasPage    `route:"/alias Alias"`
	Pointer *raAccountPage `route:"/pointer Pointer"`
}

func TestGeneratorGap_aliasAndPointerFields(t *testing.T) {
	mux := http.NewServeMux()
	if _, err := Mount(mux, gpAliasRoot{}, "/", "App", WithRequestArgs(raCurrentUser)); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/alias", "/pointer"} {
		if _, body := serve(t, mux, raGet(path, "ana")); body != "ana/ana" {
			t.Errorf("%s rendered %q, want ana/ana", path, body)
		}
	}
}

// A page that gets its component methods from an embedded struct, and an ID
// naming one of those promoted methods.
type gpBase struct{}

func (gpBase) Content() component { return testComponent{"base content"} }

type gpDerived struct{ gpBase }

func (p gpDerived) Page() component { return gpIDComp{p.Content} }

// gpIDComp renders ID(ctx, ref), or the error.
type gpIDComp struct{ ref any }

func (c gpIDComp) Render(ctx context.Context, w io.Writer) error {
	id, err := ID(ctx, c.ref)
	if err != nil {
		id = "error: " + err.Error()
	}
	_, werr := fmt.Fprint(w, id)
	return werr
}

type gpDerivedRoot struct {
	Derived gpDerived `route:"/derived Derived"`
}

func TestGeneratorGap_idOfPromotedMethod(t *testing.T) {
	mux := http.NewServeMux()
	if _, err := Mount(mux, gpDerivedRoot{}, "/", "App"); err != nil {
		t.Fatal(err)
	}
	// reflect resolves p.Content to gpBase, which is no page: an error, which
	// the other two ways must give too.
	const want = "error: cannot find page for method expression: " +
		"no page node found for type github.com/jackielii/structpages.gpBase"
	if _, body := serve(t, mux, raGet("/derived", "")); body != want {
		t.Errorf("rendered %q, want %q", body, want)
	}
}

// A Props returning an interface, and a component method on a pointer
// receiver of a page mounted as a value.
type gpLabel interface{ label() string }
type gpLabelText string

func (l gpLabelText) label() string { return string(l) }

type gpShapesPage struct{}

func (gpShapesPage) Props() (gpLabel, error)  { return gpLabelText("props"), nil }
func (gpShapesPage) Page(l gpLabel) component { return testComponent{l.label()} }
func (*gpShapesPage) Side() component         { return testComponent{"side"} }

type gpShapesRoot struct {
	Shapes gpShapesPage `route:"/shapes Shapes"`
}

func TestGeneratorGap_signatureShapes(t *testing.T) {
	mux := http.NewServeMux()
	if _, err := Mount(mux, gpShapesRoot{}, "/", "App", WithTargetSelector(HTMXRenderTarget)); err != nil {
		t.Fatal(err)
	}
	if _, body := serve(t, mux, raGet("/shapes", "")); body != "props" {
		t.Errorf("/shapes rendered %q, want props", body)
	}
	req := raGet("/shapes", "")
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Target", "shapes-side")
	if _, body := serve(t, mux, req); body != "side" {
		t.Errorf("/shapes targeting shapes-side rendered %q, want side", body)
	}
}
