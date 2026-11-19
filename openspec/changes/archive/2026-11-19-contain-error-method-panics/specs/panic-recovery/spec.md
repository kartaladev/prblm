# Spec Delta

## ADDED Requirements

### Requirement: Panic values with panicking methods are answered
When the recovered panic value is an error whose own methods panic as recovery inspects it, the recovery middleware SHALL answer like any other panic: the client SHALL receive the safe 500 problem, the hook SHALL receive the original panic, and no panic SHALL escape the middleware. A panic raised during the inspection SHALL be reported to the hook as a panic error.

#### Scenario: Hostile panic value
- **Given** a handler wrapped by the recovery middleware that panics with `<value>`
- **When** a client sends a request
- **Then** the client receives status `<status>` with `Content-Type: <type>` and the body `<body>`
- **And** the body does not contain `boom`
- **And** the hook received `<hook>`

##### Examples:
| value | status | type | body | hook |
|-------|--------|------|------|------|
| an error whose `Is` method panics with `"boom"` | 500 | `application/problem+json` | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | a panic error with value `"boom"`, then a panic error holding the value |
| an error wrapping `syscall.EPIPE` | (no response written) | (none) | (none) | a panic error holding the value |
| `"boom"` | 500 | `application/problem+json` | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | a panic error holding the value |

#### Scenario: Hostile panic value without a hook
- **Given** a handler wrapped by the recovery middleware of a handler with no hook, that panics with `<value>`
- **When** a client sends a request
- **Then** the client receives status `<status>`
- **And** the server keeps serving

##### Examples:
| value | status |
|-------|--------|
| an error whose `Is` method panics with `"boom"` | 500 |
