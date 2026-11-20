# Spec Delta

## MODIFIED Requirements

### Requirement: Translator panics are contained once
A panic in the translator function or in a translation SHALL still produce the validation problem. The first panic SHALL be the problem's cause, a `*prblm.PanicError` with its stack; later panics for the same problem SHALL use the default message without another stack. A panic SHALL be told apart by the call not returning normally, so `panic(nil)` is reported under `GODEBUG=panicnil=1` too. A panic with `http.ErrAbortHandler` SHALL NOT be recovered, so net/http aborts the response.

#### Scenario: Panicking translations
- **Given** the validation mapper registered with a translator function that `<translator>`
- **When** validation fails for a request
- **Then** the result is `<result>`

##### Examples:
| translator | result |
|------------|--------|
| panics | a 422 with `failed validation: min`, and the `OnError` hook finds a `*prblm.PanicError` as the problem's cause |
| returns a translator whose `min` translation panics, for 1,000 failed entries | a 422 with `failed validation: min` for each, and the problem's cause is a single `*prblm.PanicError` |
| panics with `http.ErrAbortHandler` | no response: the panic propagates and net/http aborts the response |
| calls `panic(nil)`, under `GODEBUG=panicnil=1` | a 422 with the default message, and the `OnError` hook finds a `*prblm.PanicError` with a nil value as the problem's cause |
| returns a translator whose translation calls `panic(nil)`, under `GODEBUG=panicnil=1` | a 422 with the default message, and the `OnError` hook finds a `*prblm.PanicError` with a nil value as the problem's cause |
| calls `panic(nil)`, by default | a 422 with the default message, and the `OnError` hook finds a `*prblm.PanicError` whose value is a `*runtime.PanicNilError` |
