# Spec Delta

## MODIFIED Requirements

### Requirement: Sentinel and typed mapping helpers
The library SHALL provide helpers that build a mapper from a sentinel error (matched as `errors.Is` does), from an error type (matched as `errors.As` does, generic over the type), and from a sentinel plus a status alone. The helpers SHALL match over the bounded walk of the error chain: typed nils never match, and an `As` method that leaves a pointer or interface target nil does not match.

#### Scenario: Sentinel helper
- **Given** a mapper built for sentinel `ErrVesselNotFound` returning status 404
- **When** the handler resolves `<error>`
- **Then** the resolved status is `<status>`

##### Examples:
| error | status |
|-------|--------|
| `ErrVesselNotFound` | 404 |
| `fmt.Errorf("load: %w", ErrVesselNotFound)` | 404 |
| `errors.Join(io.EOF, ErrVesselNotFound)` | 404 |
| `errors.New("vessel not found")` | 500 |

#### Scenario: Typed helper exposes the value
- **Given** a mapper built for type `*RefusedError` that returns status 409 with extension `reasons` from the value
- **When** the handler resolves `<error>`
- **Then** the resolved status is `<status>` and extension `reasons` is `<reasons>`

##### Examples:
| error | status | reasons |
|-------|--------|---------|
| `&RefusedError{Reasons: ["closed"]}` | 409 | `["closed"]` |
| `fmt.Errorf("x: %w", &RefusedError{Reasons: ["full"]})` | 409 | `["full"]` |
| `errors.New("refused")` | 500 | (none) |

##### Examples: typed nils and empty results
| error | status | reasons |
|-------|--------|---------|
| `errors.Join((*RefusedError)(nil), &RefusedError{Reasons: ["full"]})` | 409 | `["full"]` |
| `errors.Join((*RefusedError)(nil), prblm.ErrNotFound)` | 404 | (none) |
| an error whose `As` method returns true and leaves a `*RefusedError` target nil | 500 | (none) |

#### Scenario: Helper called directly
- **Given** a mapper built with `<helper>`
- **When** the application calls its `MapError` with `<error>` outside any handler
- **Then** the result is `<result>`
- **And** the call returns within 1 second

##### Examples:
| helper | error | result |
|--------|-------|--------|
| `prblm.Status(ErrVesselNotFound, 404)` | `fmt.Errorf("load: %w", ErrVesselNotFound)` | a problem with status 404 |
| `prblm.Status(ErrVesselNotFound, 404)` | an error whose `Unwrap() error` returns itself | nil |
| `prblm.As(func(*RefusedError) *prblm.Problem { return prblm.New(409) })` | an error whose `As` method panics with `"boom"` | nil |
| `prblm.As(func(*RefusedError) *prblm.Problem { return prblm.New(409) })` | `(*RefusedError)(nil)` | nil |
| `prblm.Status(ErrVesselNotFound, 404)` | `errors.Join(ErrVesselNotFound, an error whose As method panics with "boom")` | a problem with status 404 |

### Requirement: Self-describing errors
An error chain that contains a problem, or an error that implements the self-describing contract, SHALL resolve to that problem without calling any mapper. The outermost self-describing error in the chain SHALL win; a typed nil in the chain is never self-describing. A received problem SHALL NOT count as self-describing; a problem the application takes out of it and returns SHALL.

#### Scenario: Problem or describer in the chain
- **Given** a consumer mapper that counts its calls
- **When** the handler resolves `<error>`
- **Then** the resolved status is `<status>`
- **And** the mapper was called `<calls>` times

##### Examples:
| error | status | calls |
|-------|--------|-------|
| a problem with status 409 | 409 | 0 |
| `fmt.Errorf("op: %w", problem with status 409)` | 409 | 0 |
| a domain error whose self-description has status 422 | 422 | 0 |
| a problem 400 wrapping a describer with status 422 | 400 | 0 |
| a describer returning nil | 500 | 1 |
| a received problem holding status 404 | 502 | 1 |
| the problem held by a received problem with status 404 (`&r.Problem`) | 404 | 0 |

##### Examples: typed nils
| error | status | calls |
|-------|--------|-------|
| `(*prblm.Problem)(nil)` stored in an `error` | 500 | 1 |
| `errors.Join((*prblm.Problem)(nil), problem with status 404)` | 404 | 0 |
| `errors.Join((*DomainError)(nil), problem with status 409)`, `*DomainError` a describer | 409 | 0 |
| an error whose `As` method returns true and leaves a `prblm.Describer` target nil, wrapping a problem with status 404 | 404 | 0 |

### Requirement: Faulty consumer code never breaks resolution
A mapper, describer or enricher that panics SHALL NOT crash the request. A panicking mapper or describer SHALL be treated as no match, a panicking enricher SHALL be skipped. Each panic SHALL be reported to the hooks, whatever the panic value, `nil` included and also when the runtime recovers it as `nil` (`GODEBUG=panicnil=1`). A panicking hook SHALL be ignored, and the hooks after it SHALL still be called.

#### Scenario: Consumer code panics
- **Given** `<faulty>` and a consumer mapper `ErrX` to 409 registered after it
- **When** the handler writes `ErrX`
- **Then** the status is `<status>`
- **And** the hook received `<hook>`

##### Examples:
| faulty | status | hook |
|--------|--------|------|
| a mapper that panics, registered first | 409 | a panic error, then `ErrX` |
| a describer error that panics wrapping `ErrX` | 409 | a panic error, then `ErrX` |
| an enricher that panics | 409 | a panic error, then `ErrX` |
| a hook that panics | 409 | (hook panics, response unaffected) |
| a nil mapper in the option list | 409 | `ErrX` |
| a nil enricher in the option list | 409 | `ErrX` |
| a mapper that calls `panic(nil)`, registered first | 409 | a panic error with value `*runtime.PanicNilError`, then `ErrX` |

#### Scenario: panic(nil) in consumer code under GODEBUG=panicnil=1
- **Given** a test binary run with `GODEBUG=panicnil=1`, `<faulty>` and a consumer mapper `ErrX` to 409 registered after it
- **When** the handler writes `ErrX`
- **Then** the status is `<status>`
- **And** the hook received `<hook>`

##### Examples:
| faulty | status | hook |
|--------|--------|------|
| a mapper that calls `panic(nil)`, registered first | 409 | a panic error with value `nil`, then `ErrX` |
| an enricher that calls `panic(nil)` | 409 | a panic error with value `nil`, then `ErrX` |
| a describer error whose `Problem` method calls `panic(nil)`, wrapping `ErrX` | 409 | a panic error with value `nil`, then `ErrX` |

#### Scenario: A panicking hook does not silence the others
- **Given** a handler with `<hooks>`, where `P` panics with `<value>` and the others record their calls
- **When** the handler writes `ErrX`
- **Then** the status is 409
- **And** the recording hooks were called in the order `<calls>`, each with `ErrX`

##### Examples:
| hooks | value | calls |
|-------|-------|-------|
| `OnError(P)`, `AddErrorHook(B)` | `"boom"` | B |
| `OnError(A)`, `AddErrorHook(P)`, `AddErrorHook(C)` | `"boom"` | A, C |
| `AddErrorHook(B)`, `AddErrorHook(P)` | `nil` | B |
| `OnError(A)`, `AddErrorHook(P)`, `AddErrorHook(C)` | `http.ErrAbortHandler` | A, C |

### Requirement: Error values with panicking methods never break resolution
An error whose own methods panic while the handler resolves it SHALL NOT crash the request; this covers an `Unwrap`, `Is` or `As` method. The panic SHALL abort the walk of the chain, and the answer SHALL be the safe 500 problem, even when another step would have matched. The hooks SHALL receive one `ErrUnwalkableError` note with reason `panic`, wrapping a panic error, then the error. An `As` method reporting a match with an empty pointer or interface target SHALL count as no match, not as a panic.

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

### Requirement: Received problems are never relayed implicitly
A received problem anywhere in the error chain SHALL NOT reach the client: none of its title, detail, type, instance or extension members SHALL appear in the response. With zero configuration it SHALL resolve as `ErrBadGateway` does. Consumer and scoped mappers SHALL take precedence, and the hook SHALL receive the error as returned. A typed-nil received problem in the chain SHALL be skipped like any typed nil, so it does not resolve as `ErrBadGateway`.

#### Scenario: Gateway returns a received problem
- **Given** a handler with `<setup>`
- **And** a received problem `R` holding status 404, detail `vessel 7 missing in db shard-3` and extension `table`=`vessels_shard3`
- **When** a handler returns `<error>`
- **Then** the response status is `<status>`, the type is `<type>` and the detail is `<detail>`
- **And** the body contains the extension `table` is `<leaks>`
- **And** the hook received `<hook>`

##### Examples: safe by default
| setup | error | status | type | detail | leaks | hook |
|-------|-------|--------|------|--------|-------|------|
| zero configuration | `R` | 502 | `about:blank` | `An upstream service returned an invalid response.` | false | `R` |
| zero configuration | `fmt.Errorf("load vessel: %w", R)` | 502 | `about:blank` | `An upstream service returned an invalid response.` | false | the returned error |
| zero configuration | `fmt.Errorf("%w: %w", prblm.ErrBadGateway, R)` | 502 | `about:blank` | `An upstream service returned an invalid response.` | false | the returned error |
| zero configuration | `fmt.Errorf("%w: %w", prblm.ErrNotFound, R)` | 404 | `about:blank` | `The requested resource was not found.` | false | the returned error |
| zero configuration | `prblm.WithStatus(R, 503)` | 503 | `about:blank` | (none) | false | the returned error |
| zero configuration | `errors.Join(io.EOF, R)` | 502 | `about:blank` | `An upstream service returned an invalid response.` | false | the returned error |
| zero configuration | `fmt.Errorf("op: %w", (*prblm.Received)(nil))` | 500 | `about:blank` | (none) | false | the returned error |
| built-ins disabled | `R` | 500 | `about:blank` | (none) | false | `R` |

##### Examples: consumer decides
| setup | error | status | type | detail | leaks | hook |
|-------|-------|--------|------|--------|-------|------|
| consumer mapper `prblm.As` for `*prblm.Received` returning status 404 with detail `Vessel not found.` | `fmt.Errorf("load: %w", R)` | 404 | `about:blank` | `Vessel not found.` | false | the returned error |
| consumer mapper `prblm.Status(prblm.ErrBadGateway, 503)` | `R` | 503 | `about:blank` | (none) | false | `R` |
| scoped mapper `prblm.Status(prblm.ErrBadGateway, 503)` on the request | `R` | 503 | `about:blank` | (none) | false | `R` |
| consumer mapper `prblm.As` for `*prblm.Received` that panics | `R` | 502 | `about:blank` | `An upstream service returned an invalid response.` | false | a panic error, then `R` |
| consumer mapper `prblm.As` for `*prblm.Received` that returns nil | `R` | 502 | `about:blank` | `An upstream service returned an invalid response.` | false | `R` |

##### Examples: explicit relay
| setup | error | status | type | detail | leaks | hook |
|-------|-------|--------|------|--------|-------|------|
| zero configuration | `&R.Problem` | 404 | `about:blank` | `vessel 7 missing in db shard-3` | true | `&R.Problem` |

#### Scenario: Same response in every adapter
- **Given** `<adapter>` with zero configuration
- **And** a received problem `R` holding status 404, detail `vessel 7 missing in db shard-3` and extension `table`=`vessels_shard3`
- **When** a handler returns `fmt.Errorf("%w: %w", prblm.ErrBadGateway, R)`
- **Then** the response is status `<status>`, `Content-Type: application/problem+json` and body `<body>`

##### Examples:
| adapter | status | body |
|---------|--------|------|
| net/http | 502 | `{"title":"Bad Gateway","status":502,"detail":"An upstream service returned an invalid response.","instance":"/vessels/7"}` |
| gin middleware | 502 | `{"title":"Bad Gateway","status":502,"detail":"An upstream service returned an invalid response.","instance":"/vessels/7"}` |
| fiber error handler | 502 | `{"title":"Bad Gateway","status":502,"detail":"An upstream service returned an invalid response.","instance":"/vessels/7"}` |
