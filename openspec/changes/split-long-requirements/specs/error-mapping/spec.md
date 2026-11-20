# Spec Delta

## MODIFIED Requirements

### Requirement: Error values with panicking methods never break resolution
An error whose own `Unwrap`, `Is` or `As` method panics during resolution SHALL NOT crash the request. The panic SHALL abort the chain's walk, and the answer SHALL be the safe 500 problem even when another step would have matched. The hooks SHALL receive one `ErrUnwalkableError` note with reason `panic`, wrapping a panic error, then the error. An `As` method that reports a match but leaves a pointer or interface target empty SHALL count as no match, not as a panic.

#### Scenario: Error methods panic
- **Given** a handler with `<setup>` and a recording hook
- **When** the handler writes `<error>`
- **Then** the response status is `<status>` and the body is the problem `<body>`
- **And** the body does not contain `boom`
- **And** the hook received `<hook>`

##### Examples: zero configuration
| setup | error | status | body | hook |
|-------|-------|--------|------|------|
| zero configuration | an error whose `As` method panics with `"boom"` | 500 | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | an `ErrUnwalkableError` note wrapping a panic error with value `"boom"`, then the error |
| zero configuration | an error whose `Is` method panics with `"boom"` | 500 | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | an `ErrUnwalkableError` note wrapping a panic error with value `"boom"`, then the error |
| zero configuration | a nil pointer to an error type whose `Is` method has a value receiver | 500 | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | the error |

##### Examples: the status-hint step
| setup | error | status | body | hook |
|-------|-------|--------|------|------|
| built-ins disabled | an error whose `As` method panics with `"boom"` | 500 | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | an `ErrUnwalkableError` note wrapping a panic error with value `"boom"`, then the error |
| built-ins disabled | an error whose `As` method reports a match and leaves its target empty | 500 | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | the error |

##### Examples: another step would have matched
| setup | error | status | body | hook |
|-------|-------|--------|------|------|
| consumer mapper `prblm.Status(ErrX, 409)` | `errors.Join(ErrX, an error whose As method panics with "boom")` | 500 | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | an `ErrUnwalkableError` note wrapping a panic error with value `"boom"`, then the error |
| zero configuration | `prblm.WithStatus(an error whose Is method panics with "boom", 404)` | 500 | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | an `ErrUnwalkableError` note wrapping a panic error with value `"boom"`, then the error |
| zero configuration | `errors.Join(prblm.New(404), an error whose As method panics with "boom")` | 404 | `{"title":"Not Found","status":404,"instance":"/v1/vessels/7"}` | the error |

#### Scenario: Error methods panic without a hook
- **Given** a handler with `<setup>` and no hook
- **When** the handler writes `<error>`
- **Then** the response status is `<status>`
- **And** nothing panics out of the handler

##### Examples:
| setup | error | status |
|-------|-------|--------|
| zero configuration | an error whose `As` method panics with `"boom"` | 500 |
| built-ins disabled | an error whose `As` method panics with `"boom"` | 500 |
| zero configuration | an error whose `Unwrap() error` panics with `"boom"` | 500 |
