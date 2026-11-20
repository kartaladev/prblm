# panic-recovery Specification

## Purpose

Defines opt-in net/http middleware that turns handler panics into safe RFC 9457 500 responses, while giving the panic value and stack only to the consumer's hook.

## Requirements

### Requirement: Recovery is opt-in
Panics SHALL be recovered only when the recovery middleware is installed. The error-handling adapter alone SHALL NOT recover panics.

#### Scenario: Middleware installed or not
- **Given** a handler that panics, `<installed>`
- **When** a client sends a request
- **Then** `<outcome>`

##### Examples:
| installed | outcome |
|-----------|---------|
| wrapped by the recovery middleware | the client receives a 500 problem |
| wrapped only by the error-returning adapter | the panic propagates to net/http's own handling |

### Requirement: Panic becomes a safe 500 problem
A recovered panic SHALL produce a 500 problem with type `about:blank`, title `Internal Server Error` and no detail. The panic value and stack SHALL never appear in the response.

#### Scenario: Panic values
- **Given** a handler wrapped by the recovery middleware that panics with `<value>`
- **When** a client sends a request
- **Then** the status is 500 and the body contains no part of `<text>` and no stack frame

##### Examples:
| value | text |
|-------|------|
| `"index out of range"` | `index out of range` |
| `errors.New("db password=hunter2")` | `hunter2` |
| `42` | `42` |
| `nil` (via `panic(nil)`, a `*runtime.PanicNilError`) | `nil` |

### Requirement: Panic details go to the hook
The hook SHALL receive the panic as an error carrying the panic value and the stack captured at the panic site. When the value is an error, it SHALL be reachable through standard unwrapping.

#### Scenario: Hook receives the panic
- **Given** a handler function `loadVessel` wrapped by the recovery middleware that panics with `<value>`
- **When** a client sends a request
- **Then** the hook receives an error from which value `<value>` can be extracted
- **And** its stack contains `loadVessel`
- **And** unwrapping to `<target>` gives `<matches>`

##### Examples:
| value | target | matches |
|-------|--------|---------|
| `io.ErrClosedPipe` | `io.ErrClosedPipe` | true |
| `"boom"` | `io.ErrClosedPipe` | false |

### Requirement: Same rendering as errors
Recovered panics SHALL go through the same enrichers, negotiation and headers as other errors.

#### Scenario: Negotiated 500
- **Given** an enricher that adds `trace_id` and a request with `Accept: <accept>`
- **When** the handler panics under the recovery middleware
- **Then** the 500 problem has `Content-Type: <content_type>` and contains `trace_id`

##### Examples:
| accept | content_type |
|--------|--------------|
| (none) | `application/problem+json` |
| `application/problem+xml` | `application/problem+xml` |

### Requirement: net/http abort signal is preserved
A panic with `http.ErrAbortHandler` SHALL be re-raised unchanged so net/http aborts the response as designed, and SHALL NOT be reported as an error.

#### Scenario: Abort handler
- **Given** a handler wrapped by the recovery middleware that panics with `<value>`
- **When** a client sends a request
- **Then** the middleware `<behavior>`

##### Examples:
| value | behavior |
|-------|----------|
| `http.ErrAbortHandler` | re-panics with `http.ErrAbortHandler`, writes nothing and does not call the hook |
| `fmt.Errorf("x: %w", http.ErrAbortHandler)` | writes a 500 problem (only the exact sentinel aborts) |

### Requirement: No write after a started response or a lost connection
When the response has already started, or the panic shows the client connection is broken, recovery SHALL NOT write a problem and SHALL still report the panic to the hook.

#### Scenario: Panic after output or on broken connection
- **Given** a handler wrapped by the recovery middleware that `<does>`
- **When** a client sends a request
- **Then** the client receives `<received>`
- **And** the hook receives the panic

##### Examples:
| does | received |
|------|----------|
| writes `{"partial":` and then panics | only `{"partial":` |
| panics with an error wrapping `syscall.EPIPE` | nothing more |
| panics with an error wrapping `syscall.ECONNRESET` | nothing more |
| panics before writing anything | a 500 problem |

### Requirement: Recovery never panics
If producing the problem panics during recovery (for example, an enricher or the hook panics), the middleware SHALL write the constant minimal JSON 500 problem when nothing has been written yet, and SHALL NOT let a second panic escape.

#### Scenario: Faulty consumer code during recovery
- **Given** a handler that panics and `<faulty>`
- **When** a client sends a request under the recovery middleware
- **Then** the client receives a 500 `application/problem+json` response and the server keeps serving

##### Examples:
| faulty |
|--------|
| an enricher that panics |
| a hook that panics |
| an encoder that panics |

### Requirement: Panic values with panicking methods are answered
When the recovered panic value is an error whose chain cannot be walked, because its own methods panic or because it loops back on itself, the recovery middleware SHALL answer like any other panic. The client SHALL receive the safe 500 problem, and no panic SHALL escape the middleware. The hook SHALL receive one `ErrUnwalkableError` note naming the reason, then the original panic.

#### Scenario: Hostile panic value
- **Given** a handler wrapped by the recovery middleware that panics with `<value>`
- **When** a client sends a request
- **Then** within 1 second the client receives status `<status>` with `Content-Type: <type>` and the body `<body>`
- **And** the body does not contain `boom`
- **And** the hook received `<hook>`

##### Examples:
| value | status | type | body | hook |
|-------|--------|------|------|------|
| an error whose `Is` method panics with `"boom"` | 500 | `application/problem+json` | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | an `ErrUnwalkableError` note with reason `panic` wrapping a panic error with value `"boom"`, then a panic error holding the value |
| an error whose `Unwrap() error` returns itself | 500 | `application/problem+json` | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | an `ErrUnwalkableError` note with reason `budget`, then a panic error holding the value |
| an `errors.Join` whose entries contain the join | 500 | `application/problem+json` | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | an `ErrUnwalkableError` note with reason `budget`, then a panic error holding the value |
| an error wrapping `syscall.EPIPE` | (no response written) | (none) | (none) | a panic error holding the value |
| a typed-nil `*MyError` whose `Is` method has a value receiver | 500 | `application/problem+json` | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | a panic error holding the value |
| `"boom"` | 500 | `application/problem+json` | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | a panic error holding the value |
| `nil`, the handler calling `panic(nil)` in a binary run with `GODEBUG=panicnil=1` | 500 | `application/problem+json` | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | a panic error with value `nil` |
| none: the handler calls `runtime.Goexit`, served by a real net/http server over HTTP/1.1 | 500 | `application/problem+json` | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | a panic error with value `nil` |
| none: the handler calls `runtime.Goexit`, served by a real net/http server over HTTP/2 | 500 | `application/problem+json` | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}`, then a stream reset with `INTERNAL_ERROR`, which net/http's HTTP/2 server sends for a handler that did not return | a panic error with value `nil`, once |

#### Scenario: Hostile panic value without a hook
- **Given** a handler wrapped by the recovery middleware of a handler with no hook, that panics with `<value>`
- **When** a client sends a request
- **Then** the client receives status `<status>`
- **And** the server keeps serving

##### Examples:
| value | status |
|-------|--------|
| an error whose `Is` method panics with `"boom"` | 500 |
| an error whose `Unwrap() error` returns itself | 500 |

### Requirement: Framework-neutral panic response
The library SHALL turn a recovered panic and a request description into a complete safe-500 response without writing it, so a framework adapter answers panics exactly as the net/http recovery middleware does. It SHALL report that nothing must be written when the panic shows the client connection is broken. It SHALL walk the panic value once and report the panic to the hooks once.

#### Scenario: Panic response without writing
- **Given** a handler with a recording hook
- **When** an adapter asks for the panic response to `GET /v1/vessels/7` for a panic with `<value>`
- **Then** the result is `<result>`
- **And** the hook received `<hook>`

##### Examples:
| value | result | hook |
|-------|--------|------|
| `"boom"` | write: status 500, `Content-Type: application/problem+json`, body `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | a panic error holding the value, with the 500 problem |
| `fmt.Errorf("write: %w", syscall.EPIPE)` | do not write | a panic error holding the value, with a nil problem |
| `syscall.ECONNRESET` | do not write | a panic error holding the value, with a nil problem |
| an error whose `Unwrap() error` returns itself | write: status 500, the same body | an `ErrUnwalkableError` note with reason `budget`, then a panic error holding the value |
| an error whose `Is` method panics with `"boom"` | write: status 500, the same body | an `ErrUnwalkableError` note with reason `panic`, then a panic error holding the value |
| `nil` panic value recovered under `GODEBUG=panicnil=1` | write: status 500, the same body | a panic error with value `nil` |
| a nil `*PanicError` passed by the adapter | write: status 500, the same body | a panic error with value `nil` |

### Requirement: Nested recovery never loses a panic
Nested recovery middleware SHALL report a panic that stops at the innermost recovery once. Every outer recovery SHALL report a handler that did not return, with a nil problem when the response has started, so a second real panic is never lost. A `runtime.Goexit`, which no `recover` stops and which an outer recovery cannot tell from a new `panic(nil)` under `GODEBUG=panicnil=1`, is therefore reported by each recovery.

#### Scenario: Nested recovery
- **Given** a handler with a recording hook, and its recovery middleware wrapping its recovery middleware around a handler that `<does>`
- **When** a client sends `GET /v1/vessels/7`
- **Then** within 1 second the client receives status 500 with the body `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}`
- **And** the hook received exactly `<hook>`

##### Examples:
| does | hook |
|------|------|
| panics with `"boom"` | one panic error with value `"boom"`, with the 500 problem |
| calls `runtime.Goexit` | a panic error with value `nil` with the 500 problem, then a panic error with value `nil` with a nil problem |
| calls `runtime.Goexit`, with a middleware between the two recoveries that wraps the response writer and unwraps to it | a panic error with value `nil` with the 500 problem, then a panic error with value `nil` with a nil problem |

#### Scenario: A second panic(nil) after nested recovery
- **Given** a binary run with `GODEBUG=panicnil=1`, a handler with a recording hook, and its recovery middleware around a middleware that calls the inner recovery middleware, around a handler that calls `panic(nil)`, and then calls `panic(nil)` itself once the inner one has returned
- **When** a client sends `GET /v1/vessels/7`
- **Then** the client receives status 500 with the body `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}`
- **And** the hook received exactly a panic error with value `nil` with the 500 problem, then a panic error with value `nil` with a nil problem

#### Scenario: Goexit inside an error method
- **Given** a handler with a recording hook, whose recovery middleware wraps a handler returning an error whose `Unwrap` calls `runtime.Goexit`
- **When** a client sends `GET /v1/vessels/7`
- **Then** the client receives status 500 with the body `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}`
- **And** the hook received exactly one panic error with value `nil`, and no `ErrUnwalkableError` note: the goroutine exits before the aborted walk can be reported
