package registryutil_test

import (
	"slices"
	"testing"

	"github.com/zeabur/zbplan/pkg/registryutil"
)

var finder = registryutil.NewFinder()

func TestNormalizeAllowedRegistries(t *testing.T) {
	t.Parallel()

	got, err := registryutil.NormalizeAllowedRegistries([]string{" Quay.IO ", "gcr.io", "quay.io"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"quay.io", "gcr.io"}; !slices.Equal(got, want) {
		t.Fatalf("registries = %v, want %v", got, want)
	}
}

func TestNormalizeAllowedRegistriesRejectsInvalidHost(t *testing.T) {
	t.Parallel()

	if _, err := registryutil.NormalizeAllowedRegistries([]string{"https://quay.io"}); err == nil {
		t.Fatal("expected invalid registry error")
	}
}
