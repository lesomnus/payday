package cli

// The language a Watch filter is written in: CEL, which `ResourceService.Watch`
// evaluates for a subscription that sends one. Without this line the process
// serves Batch, and a Watch with no filter, and refuses one that has a filter.
//
// It is here and not in `cmd` for the reason the driver beside it is. `cmd` is
// imported by the sandbox for `Build`, so whatever is named there is linked
// into the page: CEL's parser, checker and interpreter, ANTLR under the parser,
// and `text/template` for a prompt nothing renders -- 10 MB of the sandbox
// module, for a service the page does not serve. `TestTheSandboxCarriesNoEngine`
// is what says so when it slips.
import _ "github.com/lesomnus/payday/resource/resourcecel"
