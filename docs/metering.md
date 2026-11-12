# Metered pricing: meterd, L402, and MPP

Aperture can sell requests one at a time, or it can sell a prepaid bundle of
usage and draw it down as requests flow. This document describes the second
mode: what the meter is, how a bundle lives and dies, and how each of the three
credential schemes settles its requests against it: L402, and the charge and
session intents of the Payment HTTP Authentication Scheme (MPP). It closes with what changes when responses stream.

The running example throughout is the deployment this design came from: an
OpenAI-compatible inference endpoint where a buyer pays 2,000 satoshis for a
bundle of 1,000,000 model tokens, then spends that bundle a few dozen tokens at
a time, one chat completion per request.

## The shape of the problem

A fixed price per request is the wrong unit for inference. One request costs
the seller a few dozen upstream tokens, the next costs four thousand, and the
seller only learns which after the response has been served. Pricing every
request at the worst case overcharges nearly everyone; pricing at the average
invites abuse.

Metering splits the problem in two. The *payment* happens once, up front, for a
bundle of usage at a known price. The *accounting* happens per request, after
the fact, from the usage the upstream actually reported. The credential the
buyer paid for stops being a ticket for one request and becomes the key to a
balance.

## The pieces

Three processes cooperate, joined by one gRPC surface:

- **Aperture** terminates the payment protocols, proxies requests to the
  backend, and observes responses. It holds no pricing or balance state of its
  own.
- **A price server** implements the `pricesrpc` service. It quotes prices,
  books bundles, admits or refuses requests against their balances, and debits
  reported usage. Aperture is configured to consult it per service via
  `dynamicprice` in the service stanza; setting `metered: true` there is what
  turns on the per-request flow described below.
- **meterd** is the reference price server that ships in this repository. It
  keeps its bundles in a JSON state file and its rates in static config, which
  is enough for development and small deployments. Production deployments
  embed the same `meterd.Server` behind their own storage and rate source: the
  `Store` and `RateSource` interfaces are the two seams, so a durable
  implementation replaces the state file with a database and the static rates
  with a live price feed without touching the protocol logic.

The `pricesrpc` calls, in the order a bundle meets them:

| RPC | Who calls it, when | What it does |
|---|---|---|
| `GetPrice` | Aperture, before minting a 402 | Quotes the bundle price for this request's model |
| `ChallengeMinted` | Aperture, as the 402 is minted | Books a bundle under the challenge's token ID |
| `AuthorizeRequest` | Aperture, per authenticated request | Admits or refuses against the balance; reserves an estimate |
| `ReportUsage` | Aperture, when the response finishes | Debits actual usage; releases the reservation |
| `QuoteSession` / `SettleSession` | Aperture, for MPP sessions only | Session accounting; described in its own section below |

## The life of a bundle

A bundle is born when a 402 is minted, not when it is paid. `GetPrice` reads
the request body, resolves the model, and quotes the bundle: at the blended
rate of (input + output) / 2 millisatoshis per token, 1,000,000 tokens of a
model priced at 1 and 2 msat comes to 1,500 sats. `ChallengeMinted` then books
that bundle under the challenge's token ID, so that when the payment arrives
there is already a balance for the credential to unlock. A booking whose
challenge is never paid expires quietly after ten minutes; booking is cheap,
and most 402s are minted for buyers who already hold a credential and simply
retried too late.

From then on, every authenticated request runs the same loop:

1. `AuthorizeRequest` *reserves* the request's `max_tokens` (or
   `estimatedtokens`, default 4,096) plus one token per byte of request body
   for the prompt, which the upstream bills regardless of `max_tokens`. The
   body size is the declared `Content-Length`, so the prefix aperture
   forwards cannot shrink it; a cut body with no declared length reserves the
   whole bundle. The reservation is a hold, not a charge. Alongside it the
   server returns a *settle estimate*, the same completion part plus one
   prompt token per four body bytes, capped at the reservation; with the body
   size unknown it is the reservation, since the unseen bytes could be any
   size. Aperture echoes both in the usage report.
2. The request proxies to the backend, and aperture wraps the response body so
   the final bytes are captured as they pass. Nothing is buffered and nothing
   is rewritten; the wrapper keeps a bounded tail (16 KiB by default,
   `usagetailbytes` to change it) of what flowed.
3. When the body finishes, `ReportUsage` carries that tail to the price
   server. The server extracts the usage object the upstream reported, debits
   the true token count, and releases the exact reservation. When the
   prompt/completion split is known, the debit is weighted by direction, so an
   all-output workload pays the output rate rather than hiding behind the
   blend. The division rounds up: the msat value drawn from the bundle always
   covers the msat value served. Aperture makes the first report attempt
   before the response ends, so a buyer that sends its next request as soon
   as the previous one finishes finds that reservation already released;
   only retries of a failed report run in the background.

A usage report can be lost (aperture gives up retrying, or restarts mid
request), which would hold its reservation until meterd restarts. So a
reservation is dropped after `reservationttl` (default 30 minutes, negative to
disable).

A request is admitted only if the balance left after in-flight reservations
covers its whole reservation, or if nothing else is in flight and any balance
is left, so the last few tokens are not stranded. That lone request may cost
more than what is left; its debit retires the bundle.

The reservation only bounds a request's cost when it has a `max_tokens` the
price server can read. Aperture does not rewrite bodies, so a request without
one, or with one past the forwarded prefix, can use more than it reserved.

When a bundle is spent, `AuthorizeRequest` refuses the request with the price
of the next bundle and aperture mints a fresh 402. If the bundle still has a
balance but in-flight reservations leave too little for this request, it sets
`retry_later` instead, and aperture answers 429 with `Retry-After`, so the
buyer does not pay for a new bundle while the current one is funded.
Exhaustion, not any property of the credential, is what ends a bundle's life.

The retry signal depends on the store dropping leaked reservations, since a
reservation that never clears would leave the buyer retrying forever. The
built-in JSON store reports this from `reservationttl`, so a negative value
keeps the 402. A daemon that embeds meterd with its own `Store` keeps the 402
unless that store implements `ReservationExpirer` and returns true from
`ReservationsExpire`.

One consequence deserves emphasis: the credential and the balance are separate
things joined by a key. Everything below is about what that key is for each
scheme, because the key is where the schemes differ.

## How L402 requests are metered

An L402 credential is a macaroon plus the preimage of the invoice the buyer
paid. The macaroon's identifier embeds a random token ID, and that ID is the
metering key: `ChallengeMinted` reads it out of the freshly minted challenge
macaroon, and every later request's ID is read back out of the credential the
buyer presents.

The buyer may present that credential three ways, and metering accepts all
three, because the authenticator does: the `Authorization: L402` header
carrying macaroon and preimage separately, or a hex macaroon with the preimage
as a caveat in either the `Macaroon` or `Grpc-Metadata-Macaroon` header. A
form the authenticator accepts but the meter did not see would be unlimited
free service, so the two lists are deliberately the same list.

Replaying an L402 credential is not an attack here; it is the product. The
buyer pays once, then presents the same macaroon and preimage on every request
until the bundle refuses. A malformed credential on a metered service is
refused outright rather than passed through unmetered, for the same reason the
header lists match: on a metered service, "not metered" and "free" are the
same word.

## How MPP charge requests are metered

The Payment HTTP Authentication Scheme (MPP) arrives alongside L402 when
aperture runs with `--authenticator.enablempp`: the same 402 then carries an
LSAT offer, an L402 offer, and a Payment offer, and the buyer chooses a door.
The Payment charge offer mints its own invoice, distinct from the L402
offer's, which raises the question the L402 section never had to ask: what key
joins this door to a bundle?

The answer is the payment hash. The charge offer's request object carries the
invoice and its payment hash; `ChallengeMinted` books a second bundle for the
same 402, keyed by that hash. At request time the key is derived from the
credential itself: the payload's preimage hashes to exactly the payment hash
of the invoice it settled, so the proof of payment and the bundle key are the
same 32 bytes. Nothing needs to be looked up and nothing needs to be trusted
from the echoed challenge; the preimage was already verified by the
authenticator before metering runs.

Each 402 therefore books two bundles, one per door, at the same price.
Whichever offer the buyer pays is the bundle that gets drawn down; the sibling
booking is never activated and expires with the other unpaid bookings.

Charge credentials on a metered service are also re-presentable, which is a
deliberate departure from the scheme's default. The Payment spec treats a
charge as buying one request, and on unmetered services aperture enforces
exactly that: a consumed charge stays consumed. On a metered service that rule
would strand the bundle, since the buyer paid for a million tokens and could
present the credential once. So the single-use rule yields to the same rule
L402 lives by: the credential stays presentable, the bundle draw-down is the
spend, and the pricer refusing an exhausted bundle is what retires the
credential. The consumption record is still written on first use, so the audit
trail survives the policy.

A charge credential is not service-bound the way an L402 macaroon is (the
macaroon carries a service caveat; the Payment challenge HMAC covers realm,
method, intent, request, and expiry, but no service name). Model mismatch
still refuses cross-model use, and the buyer can never spend more than the one
balance they paid for, so the exposure is attribution across same-model
metered services rather than overspend. Binding the service into the
challenge's `opaque` parameter, which the HMAC covers and the client must echo
verbatim, is the natural tightening when a deployment needs it.

## How MPP sessions are metered

Sessions are the third door, enabled with `--authenticator.enablesessions`,
and they run on different accounting because they solve a different problem: a
charge pays per request and a bundle pays per model, but a session holds a
*deposit* that many requests draw against, with a refund of whatever is left
when the buyer closes it.

The division of labor is inverted from the bundle flow. Aperture's session
store owns the balance: the deposit credited at open, the estimated deduction
per bearer request, the top-up credits, the refund at close. The price server
is consulted twice per request rather than asked to keep state:

- `QuoteSession` prices one request up front, and aperture deducts that
  estimate from the session balance when it admits the bearer (a request
  riding the open session, presenting its ID and deposit preimage rather
  than a fresh payment).
- `SettleSession` runs when the response finishes, with the same captured
  tail the bundle flow uses. The price server recomputes the true cost from
  the reported usage, and aperture reconciles the estimate against it, so the
  session ends up debited for what was actually served.

When the tail carries no parseable usage, the session settles at the estimate
rather than at zero. That conservative fallback predates the streaming work
and is the model the bundle path now follows for abandoned streams.

## Streaming

Streamed responses change nothing about the protocol and two things about the
mechanics.

The first is where the usage lives. A JSON response carries its usage object
in the body; an SSE stream carries it in the final data chunk, when the client
asked for it with `stream_options: {"include_usage": true}`, and some
upstreams also report running totals on every chunk. The captured tail is
parsed accordingly: the extractor walks the `data:` lines, takes the last
usage-bearing chunk, and falls back to brace-matching a usage object out of a
tail that got truncated mid-line. Aperture also strips the client's
`Accept-Encoding` on metered requests so the observed tail is plaintext; a
gzip tail would parse as nothing, and nothing would be billed. If a provider's
final chunk can exceed 16 KiB (large tool-call arguments stacked on top of the
usage object), raise `usagetailbytes`.

The second is what happens when nobody waits for that final chunk. A client
that disconnects mid-generation takes the usage object with it: the upstream
already produced (and billed the seller for) every token up to the
disconnect, and the tail holds content chunks with no usage. Those responses,
incomplete but successful and non-empty, settle at the settle estimate: the
completion ceiling the request named plus a realistic prompt figure. The
reservation's one token per byte prompt bound is kept for admission only,
since it overstates a text prompt about fourfold and would make walking away
from a large prompt cost several times what it should. A response with no
bytes still costs nothing, and a report that does not echo a settle estimate
settles at the reservation. Upstreams that report running totals per chunk
sidestep the whole question, since even an abandoned tail then carries a
parseable count and the exact partial usage is billed.

One operator note belongs with this: aperture applies its configured
`writetimeout` to proxied responses as a rolling window rather than a single
absolute deadline, pushing the connection's write deadline forward on each
write and refusing the extension once the gap between writes exceeds the
window. A streamed generation therefore lives as long as it keeps flowing and
dies when it stalls. Size `writetimeout` to the worst-case gap between
chunks, not to total stream duration: a reasoning model that thinks silently
for longer than the window is indistinguishable from a stall and is cut off.

## Configuration quick reference

On the aperture side, per service:

```yaml
services:
  - name: "inference"
    # ...
    dynamicprice:
      enabled: true
      grpcaddress: "127.0.0.1:10025"
      insecure: true
      metered: true
```

and globally, the knobs streaming cares about: `writetimeout` (the rolling
idle window described above) and the authenticator flags `enablempp` and
`enablesessions` for the Payment doors.

On the meterd side: `bundletokens` (bundle size), `estimatedtokens` (the
completion part of the reservation when a request names no `max_tokens`),
`promptbytespertoken` (body bytes per prompt token in the reservation,
default 1; does not bound images), `reservationttl` (how long a reservation is
held before it is presumed leaked),
`usagetailbytes` (tail capture, up to 1 MiB), and the per-model
`inputmsatpertoken` / `outputmsatpertoken` rates. `meterd --help` carries the
full list, and `sample-conf.yaml` shows the service stanza in context.
