// Package memory is the assistant's memory (OC-87): the conversation
// context fed back to the model, volatile facts that last for one session,
// and a persistent profile of user facts in SQLite.
//
// Facts are learned after a turn by a small model call whose proposals pass
// through hard-coded rules (rules.go) before anything is stored: secrets,
// credentials, payment data, health data and identity numbers never reach
// the database, whatever the model proposes. Nothing here logs fact or turn
// text; logs carry technical codes only.
package memory
