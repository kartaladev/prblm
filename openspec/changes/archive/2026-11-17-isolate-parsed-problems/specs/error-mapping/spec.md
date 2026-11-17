# Spec Delta

## MODIFIED Requirements

### Requirement: Self-describing errors
An error chain that contains a problem, or an error that implements the self-describing contract, SHALL resolve to that problem without calling any mapper. The outermost self-describing error in the chain SHALL win. A received problem SHALL NOT count as self-describing; a problem the application takes out of it and returns SHALL.

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

## ADDED Requirements

### Requirement: Received problems are never relayed implicitly
A received problem anywhere in the error chain SHALL NOT reach the client: none of its title, detail, type, instance or extension members SHALL appear in the response. With zero configuration it SHALL resolve as `ErrBadGateway` does. Consumer and scoped mappers SHALL take precedence, and the hook SHALL receive the error as returned.

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
| zero configuration | `fmt.Errorf("op: %w", (*prblm.Received)(nil))` | 502 | `about:blank` | `An upstream service returned an invalid response.` | false | the returned error |
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
