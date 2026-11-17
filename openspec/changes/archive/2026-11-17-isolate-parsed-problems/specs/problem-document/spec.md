# Spec Delta

## MODIFIED Requirements

### Requirement: Parse a problem from an HTTP response
The library SHALL decode a problem from an HTTP response, choosing JSON or XML by the response `Content-Type`, and SHALL return it as a received problem that holds the decoded problem. It SHALL return an error for any other media type or an unreadable body.

#### Scenario: Parse response
- **Given** an HTTP response with `Content-Type: <content_type>` and body `<body>`
- **When** it is parsed
- **Then** the result is `<result>`

##### Examples: parsed
| content_type | body | result |
|--------------|------|--------|
| `application/problem+json` | `{"status":404,"title":"Not Found"}` | a received problem holding status 404 |
| `application/problem+json; charset=utf-8` | `{"status":409}` | a received problem holding status 409 |
| `application/problem+xml` | `<problem xmlns="urn:ietf:rfc:7807"><status>404</status></problem>` | a received problem holding status 404 |

##### Examples: refused
| content_type | body | result |
|--------------|------|--------|
| `text/html` | `<html></html>` | not-a-problem error |
| (none) | `{"status":404}` | not-a-problem error |
| `application/problem+json` | a body whose read fails | read error |
| `application/problem+json` | `{"status":` | decode error |

## ADDED Requirements

### Requirement: A received problem is an error that does not describe itself
A received problem SHALL implement the Go `error` interface, with a message that names it as received and includes the held problem's message, and SHALL expose the held problem. It SHALL NOT implement the self-describing contract. Reading the message of a nil received problem SHALL NOT panic.

#### Scenario: Received problem as an error
- **Given** a received problem `<received>`
- **When** its error message is read and it is checked for the self-describing contract
- **Then** the message is `<message>`
- **And** it describes itself is `<describes>`

##### Examples:
| received | message | describes |
|----------|---------|-----------|
| holding status 404, title `Not Found`, detail `vessel 7 missing in db shard-3` | `prblm: received problem: Not Found: vessel 7 missing in db shard-3` | false |
| holding an empty problem | `prblm: received problem: problem status 0` | false |
| a nil received problem pointer | `<nil received problem>` | false |
