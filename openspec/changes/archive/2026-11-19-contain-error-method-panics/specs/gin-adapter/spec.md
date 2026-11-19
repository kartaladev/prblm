# Spec Delta

## ADDED Requirements

### Requirement: Recovery answers panic values with panicking methods
When a handler panics with an error whose own methods panic as the gin recovery middleware inspects it, the middleware SHALL answer like any other panic: the client SHALL receive the core's safe 500 problem, the hook SHALL receive the panic, and no panic SHALL escape to gin or net/http.

#### Scenario: Hostile panic value under gin
- **Given** a gin engine with `<chain>` and a handler that panics with `<value>`
- **When** a client sends a request
- **Then** the client receives status `<status>` with `Content-Type: <type>` and the body `<body>`
- **And** the body does not contain `boom`
- **And** the hook received `<hook>`

##### Examples:
| chain | value | status | type | body | hook |
|-------|-------|--------|------|------|------|
| recovery, then middleware | an error whose `Is` method panics with `"boom"` | 500 | `application/problem+json` | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | an error that unwraps to a panic error holding the value, then an error that unwraps to a panic error with value `"boom"` |
| recovery only | an error whose `Is` method panics with `"boom"` | 500 | `application/problem+json` | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | an error that unwraps to a panic error holding the value, then an error that unwraps to a panic error with value `"boom"` |
| recovery, then middleware | an error wrapping `syscall.EPIPE` | (no response written) | (none) | (none) | an error that unwraps to a panic error holding the value |
