# Walk every page in Go

`page.All` turns a generated `List` method into a lazy Go iterator. A list still
fetches bounded pages; the iterator keeps at most one response page and the
continuation tokens it has visited. It does not collect the whole result.

```go
import "github.com/lesomnus/payday/page"

req := app.RobotListRequest_builder{Size: 100}.Build()
for robot, err := range page.All(ctx, server.Robot().List, req) {
    if err != nil {
        return err
    }
    if err := process(ctx, robot); err != nil {
        return err
    }
}
```

The request is cloned when iteration starts. Its filters, order, direction,
page size, selection and starting `after` remain in effect; the caller's request
is not consumed or modified. Each new traversal begins from that request's
starting cursor. Use a fresh request to start from the beginning.

For a generated gRPC client, adapt the method with `page.Client`. Call options
apply to every page:

```go
client := app.NewRobotServiceClient(conn)
for robot, err := range page.All(ctx,
    page.Client(client.List, grpc.WaitForReady(true)), req) {
    if err != nil {
        return err
    }
    fmt.Println(robot.GetAlias())
}
```

No RPC runs until the loop starts. `break` stops immediately, including in the
middle of a response page. The next page is only requested after every item
in the current page has been consumed. Use the context deadline or cancellation
to stop work on a page already in flight. An empty page is followed when its
`next` is nonempty; an empty `next` ends the traversal.

An error is yielded once with a nil row, then iteration ends. Check the error
before using the row. Pages already consumed are not rolled back or retried.
A repeated continuation token yields `page.ErrCursor` rather than looping
forever. This is traversal of ordinary `List` calls, not a new database
snapshot; concurrent changes follow that list's cursor semantics.

## Examples

For a part's revisions, put the part reference in the generated filter once,
then range over `page.All` with that request. The filter stays on every page.
For a subject's approvals, use the same pattern to check each result and
`break` as soon as the application's condition is met. For an export, write
each yielded row directly to the output and stop on its first error instead
of retaining all rows in memory.

If the operation must remember exactly which rows were selected at one point
in time, provide that consistency in the application, for example through a
snapshot or a suitable transaction. The iterator does not create one or relax
the caller's existing authorization scope.
