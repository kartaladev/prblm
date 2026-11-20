# Spec Delta

## REMOVED Requirements

### Requirement: Fiber errors are status hints without their message
**Reason**: Split into requirements that each state one behavior; its text was over 500 characters. The behavior is unchanged.
**Migration**: None for consumers. The behavior and every scenario move to "Fiber error codes are status hints" and "Errors the adapter cannot walk get no fiber hint".

### Requirement: Opt-in recovery returns panics to the error handler
**Reason**: Split into requirements that each state one behavior; its text was over 500 characters. The behavior is unchanged.
**Migration**: None for consumers. The behavior and every scenario move to "Opt-in recovery turns panics into errors" and "A recovered panic is never a route-not-found error".

## ADDED Requirements

### Requirement: Fiber error codes are status hints
A `*fiber.Error` in the error chain SHALL supply its code as the status hint, fiber's own routing errors included, and its message SHALL never appear in the response. The adapter SHALL find it with a bounded walk that reports nothing. The hint SHALL NOT be an entry of the error's chain, so it costs nothing of the core's walk budget.

#### Scenario: Fiber-generated and handler fiber errors
- **Given** a fiber app whose error handler is the adapter, with a route `GET /v1/vessels/:id`
- **When** a client sends `<request>`
- **Then** the response status is `<status>`, the title is `<title>` and the body does not contain `<hidden>`

##### Examples: routing
| request | status | title | hidden |
|---------|--------|-------|--------|
| `GET /v1/unknown` | 404 | `Not Found` | `Cannot GET` |
| `DELETE /v1/vessels/7` | 405 | `Method Not Allowed` | `Method Not Allowed` in detail |

##### Examples: handler-returned
| request | status | title | hidden |
|---------|--------|-------|--------|
| `GET /v1/vessels/7` returning `fiber.NewError(403, "token abc expired")` | 403 | `Forbidden` | `token abc` |
| `GET /v1/vessels/7` returning `fiber.ErrBadRequest` | 400 | `Bad Request` | (none) |
| `GET /v1/vessels/7` returning `fiber.NewError(200, "ok")` | 500 | `Internal Server Error` | `ok` in detail |
| `GET /v1/vessels/7` returning `errors.Join((*fiber.Error)(nil), fiber.ErrConflict)` | 409 | `Conflict` | (none) |

#### Scenario: Mapper takes precedence over a fiber error code
- **Given** a mapper `ErrLocked` to 423
- **When** a handler returns `fmt.Errorf("%w: %w", fiber.ErrBadRequest, ErrLocked)`
- **Then** the response status is `<status>`

##### Examples:
| status |
|--------|
| 423 |

### Requirement: Errors the adapter cannot walk get no fiber hint
An error whose chain the adapter's walk cannot finish SHALL get no hint and SHALL be answered as the core resolves it. When the core's walk cannot finish either, the answer SHALL be the core's full safe 500 problem with the hook calls, never a bare status; otherwise the error SHALL be answered as usual, with no note.

#### Scenario: Errors that cannot be walked
- **Given** a fiber app whose error handler is the adapter with a recording hook
- **When** `GET /v1/vessels/7` returns `<error>`
- **Then** within 1 second the response status is 500 with `Content-Type: application/problem+json` and the body `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}`
- **And** the body does not contain `boom`
- **And** the hook received `<hook>`

##### Examples:
| error | hook |
|-------|------|
| an error whose `As` method panics with `"boom"` | an `ErrUnwalkableError` note with reason `panic` wrapping a panic error with value `"boom"`, then the error |
| `fmt.Errorf("%w: %w", fiber.ErrBadRequest, an error whose As method panics with "boom")` | an `ErrUnwalkableError` note with reason `panic` wrapping a panic error with value `"boom"`, then the error |
| an error whose `Unwrap() error` returns itself | an `ErrUnwalkableError` note with reason `budget`, then the error |
| an `errors.Join` whose entries contain `fiber.ErrBadRequest` and the join | an `ErrUnwalkableError` note with reason `budget`, then the error |

#### Scenario: Errors only the adapter cannot walk
- **Given** a fiber app whose error handler is the adapter with a recording hook
- **When** `GET /v1/vessels/7` returns `<error>`
- **Then** within 1 second the response status is `<status>` with `Content-Type: application/problem+json`
- **And** the hook received `<hook>`

##### Examples:
| error | status | hook |
|-------|--------|------|
| an error wrapping `prblm.ErrNotFound` whose `As` method panics only when asked for a `*fiber.Error` | 404 | the error alone, with no note |

#### Scenario: Route-not-found check cannot finish
- **Given** a fiber app whose error handler is the adapter with discovery on (namespace `/v1/api/probs`, type `out-of-credit` registered) and a recording hook
- **When** a request to `/v1/api/probs/out-of-credit`, which has no route, ends with an error wrapping `fiber.ErrNotFound` whose `Is` method panics only when asked for `fiber.ErrNotFound`
- **Then** within 1 second the response is a 404 `application/problem+json` problem, not the documentation
- **And** the hook received the error alone, with no note

### Requirement: Opt-in recovery turns panics into errors
The fiber adapter SHALL offer a recovery middleware that turns a handler panic into an error returned to the error handler. That error SHALL unwrap to the core panic error (value and panic-site stack), and its message SHALL NOT contain the panic value. Recovery SHALL NOT be active unless installed. A panic SHALL be told apart by the handlers not returning, not by the value `recover` returns, so a `panic(nil)` under `GODEBUG=panicnil=1` is answered and reported like any other panic.

#### Scenario: Recovery with the adapter's error handler
- **Given** a fiber app with the adapter's error handler, `<chain>`, and a handler `loadVessel` that panics with `<value>`
- **When** a client sends a request with `Accept: <accept>`
- **Then** the client receives `<received>`
- **And** the hook receives `<hook>`

##### Examples:
| chain | value | accept | received | hook |
|-------|-------|--------|----------|------|
| the recovery middleware | `"boom"` | (none) | a 500 `application/problem+json` problem without `boom` | a panic error with value `"boom"` and a stack containing `loadVessel` |
| the recovery middleware | `io.ErrClosedPipe` | `application/xml` | a 500 `application/problem+xml` problem | an error matching `io.ErrClosedPipe` |
| the recovery middleware | `http.ErrAbortHandler` | (none) | a 500 `application/problem+json` problem | a panic error with value `http.ErrAbortHandler` |
| no recovery middleware | `"boom"` | (none) | fiber's own panic behavior (the library does not recover) | nothing |

##### Examples: hostile panic values
| chain | value | accept | received | hook |
|-------|-------|--------|----------|------|
| the recovery middleware | an error whose `Unwrap() error` returns itself | (none) | within 1 second, status 500 with `Content-Type: application/problem+json` and the body `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | an `ErrUnwalkableError` note with reason `budget`, then an error that unwraps to a panic error holding the value |
| the recovery middleware | `nil`, the handler calling `panic(nil)` in a binary run with `GODEBUG=panicnil=1` | (none) | within 1 second, status 500 with `Content-Type: application/problem+json` and the body `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | an error that unwraps to a panic error with value `nil` |

#### Scenario: Recovery with fiber's default error handler
- **Given** a fiber app with the recovery middleware but fiber's default error handler
- **When** a handler panics with `<value>`
- **Then** the response status is 500 and the body does not contain `<value>`

##### Examples:
| value |
|-------|
| `"db password=hunter2"` |
| `errors.New("secret")` |

### Requirement: A recovered panic is never a route-not-found error
A recovered panic SHALL never be taken for fiber's route-not-found error, whatever its value wraps: with discovery on, a panic on a documentation path no route claims SHALL get the safe 500 problem, not the documentation, and the hooks SHALL receive it.

#### Scenario: Panic on an unrouted documentation path
- **Given** a fiber app whose error handler is the adapter with discovery on (namespace `/v1/api/probs`, type `out-of-credit` registered) and a recording hook, the recovery middleware, and a middleware that panics with `<value>`
- **When** a client sends `GET /v1/api/probs/out-of-credit`, which has no route
- **Then** within 1 second the client receives `<received>`
- **And** the hook receives `<hook>`

##### Examples:
| value | received | hook |
|-------|----------|------|
| `fmt.Errorf("tenant lookup: %w", fiber.ErrNotFound)` | a 500 `application/problem+json` problem, not the documentation | an error that unwraps to a panic error holding the value |
| `fiber.ErrNotFound` | a 500 `application/problem+json` problem, not the documentation | an error that unwraps to a panic error holding the value |
