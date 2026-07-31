// Command gen-mpp-vectors writes the cross-implementation test vectors for the
// Payment HTTP Authentication Scheme.
//
// The vectors are produced by calling the exported mpp functions, so the file
// records what this implementation actually does. Regenerate it whenever the
// wire behaviour of the mpp package changes, and hand the result to the other
// implementations of the scheme so they can replay it:
//
//	go run ./cmd/gen-mpp-vectors
//
// The mppvectors package's test fails when the committed file no longer matches
// what the generator produces, which is what keeps the two from drifting apart
// unnoticed.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/lightninglabs/aperture/mpp/mppvectors"
)

// defaultOutput is where the committed copy of the vectors lives, relative to
// the repository root.
const defaultOutput = "mpp/testdata/vectors.json"

func main() {
	output := flag.String(
		"o", defaultOutput, "path to write the vector file to, or "+
			"\"-\" for standard output",
	)
	flag.Parse()

	if err := run(*output); err != nil {
		fmt.Fprintf(os.Stderr, "gen-mpp-vectors: %v\n", err)
		os.Exit(1)
	}
}

// run generates the vectors and writes them to the given path.
func run(output string) error {
	vectors, err := mppvectors.Generate()
	if err != nil {
		return fmt.Errorf("generating vectors: %w", err)
	}

	if output == "-" {
		_, err := os.Stdout.Write(vectors)
		return err
	}

	if err := os.WriteFile(output, vectors, 0644); err != nil {
		return fmt.Errorf("writing %s: %w", output, err)
	}

	fmt.Fprintf(
		os.Stderr, "gen-mpp-vectors: wrote %d bytes to %s\n",
		len(vectors), output,
	)

	return nil
}
