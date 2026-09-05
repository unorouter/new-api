package setting

// Total a card can put onto an account during its first 24 hours, in USD,
// across every card processor. A stolen card only pays for the thief if the
// first day is worth burning through; fresh accounts here average a few
// dollars. Crypto is not capped because it cannot be charged back. 0 disables.
var CardTopUpNewAccountCapUSD = 25.0
