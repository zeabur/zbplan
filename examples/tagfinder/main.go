package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/zeabur/zbplan/pkg/registryutil"
)

const usage = `Usage:
  tagfinder tags   [-registry docker.io] [-n 5] <image> <keyword>
  tagfinder search [-registry docker.io] [-n 5] <keyword>
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}

	subcmd := os.Args[1]
	args := os.Args[2:]

	fs := flag.NewFlagSet(subcmd, flag.ExitOnError)
	registry := fs.String("registry", registryutil.RegistryDockerHub, "registry (docker.io, ghcr.io, …)")
	n := fs.Int("n", 5, "number of results")
	platform := fs.String("platform", "linux/amd64", "platform for multi-arch images (e.g. linux/arm64)")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	finder := registryutil.NewFinder(registryutil.WithPlatform(*platform))
	ctx := context.Background()

	switch subcmd {
	case "tags":
		if fs.NArg() < 2 {
			fmt.Fprintln(os.Stderr, "tags requires <image> <keyword>")
			os.Exit(1)
		}
		image, keyword := fs.Arg(0), fs.Arg(1)
		tags, err := finder.Tags(ctx, *registry, image, keyword, *n)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		if len(tags) == 0 {
			fmt.Println("(no matching tags found)")
			return
		}
		for _, t := range tags {
			fmt.Printf("%-40s  %s\n", t.Name, t.CreatedAt.Format("2006-01-02T15:04:05Z07:00"))
		}

	case "search":
		if fs.NArg() < 1 {
			fmt.Fprintln(os.Stderr, "search requires <keyword>")
			os.Exit(1)
		}
		keyword := fs.Arg(0)
		images, err := finder.Images(ctx, *registry, keyword, *n)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		if len(images) == 0 {
			fmt.Println("(no results)")
			return
		}
		for _, img := range images {
			fmt.Printf("%-40s  %s\n", img.Name, img.Description)
		}

	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n%s", subcmd, usage)
		os.Exit(1)
	}
}
