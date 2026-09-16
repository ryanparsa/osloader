// Command osloader lists and downloads operating-system installers, then
// verifies that what landed on disk is genuinely the vendor's.
package main

import "github.com/ryanparsa/osloader/internal/cli"

func main() {
	cli.Execute()
}
