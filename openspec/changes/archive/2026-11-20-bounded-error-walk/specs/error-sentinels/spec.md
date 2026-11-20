# Spec Delta

## MODIFIED Requirements

### Requirement: Degenerate errors never break resolution
Resolving nil, typed-nil and faulty custom errors SHALL NOT panic or hang. A typed nil SHALL be skipped without calling its methods. A custom `Is` method that panics SHALL abort the walk of the chain: the response SHALL be the safe 500, and the hook SHALL receive one `ErrUnwalkableError` note, then the returned error.

#### Scenario: Degenerate inputs
- **Given** a handler with a hook that records its calls
- **When** a handler returns `<error>`
- **Then** the response is `<response>`
- **And** the hook received `<hook>`

##### Examples:
| error | response | hook |
|-------|----------|------|
| `nil` | nothing written | (nothing) |
| `prblm.WithStatus(nil, 404)` | nothing written | (nothing) |
| a typed-nil `*MyError` (no `Is` method) stored in an `error` | a 500 problem without detail | the returned error |
| a typed-nil `*MyError` whose `Is` method has a value receiver | a 500 problem without detail | the returned error |
| `errors.Join((*MyError)(nil), prblm.ErrNotFound)` | a 404 problem with detail `The requested resource was not found.` | the returned error |
| an error whose `Is` method panics | a 500 problem without detail | an `ErrUnwalkableError` note with reason `panic`, then the returned error |
| an error whose `Unwrap() error` returns itself and whose `Is` method reports `prblm.ErrNotFound` | a 500 problem without detail | an `ErrUnwalkableError` note with reason `budget`, then the returned error |
