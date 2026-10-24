// Package oauth is coachd's built-in OAuth 2.1 authorization server for the
// public lane (COACHD_PUBLIC_AUTH=builtin). An MCP client gets a token only
// after the runner, signed in with a passkey, approves it on a consent page.
//
// Clients identify themselves with Client ID Metadata Documents: the
// client_id is an https URL that coachd fetches. That URL is chosen by
// whoever starts the connection, so the fetch runs only for the signed-in
// runner, through a dialer that refuses every address that is not public
// unicast, with no telemetry instrumentation, and its errors never carry the
// URL.
//
// No agent path to the watch: a token from this package can never approve,
// deliver or withdraw a workout.
package oauth
