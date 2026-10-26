# Call reports

Voxie writes down how every call went, so you can see where callers had a bad
time and whether a change made things better. It never writes down what was
said.

```
$ voxie report reports
Calls: 3, 2 minutes; ended by the caller 1, the agent 2, an error 0

No sign of trouble on 89% of 9 answered turns. Reply time p50 1.8s, p95 7.0s.

By language
      turns  no trouble   p50   p95  slow  silent  repeat  check-in  cut early  flip  unsure
  en      3         67%  1.5s  7.0s     1       0       0         0          0     0       0
  hi      3        100%  1.7s  2.5s     0       0       0         0          0     0       0
  ta      3        100%  1.9s  2.1s     0       0       0         0          0     0       0

By voice
             turns  no trouble   p50   p95  slow  silent  ...
  vibevoice      6        100%  1.8s  2.5s     0       0
   cartesia      3         67%  1.5s  7.0s     1       0
...
```

That's three scripted test calls. The English 7.0 s was one turn: the voice
router sent no audio within 3 s, so the sentence went to Cartesia. The server
log says so too, but the report is where you'd notice it happening every day.

## What's recorded

One JSON line per reply and one per call, in `reports/calls-YYYY-MM-DD.jsonl`
(set by `[reports] dir`; `enabled = false` turns them off). The compose files
mount `./reports` into the server container, so reports outlive it.

**Per reply** (`"type": "turn"`):

| Field | Meaning |
|---|---|
| `lang`, `reply_lang` | The caller's language, and the reply's `[lang:xx]` |
| `listener`, `confidence` | Which speech-to-text heard the turn, and how sure it was |
| `voice` | Which voice spoke the reply's first sentence (with a failover chain, the one that actually did) |
| `ms` | Where the time went, from the caller's voice ending to the reply's first audio: `stt`, `check` (language check), `merge`, `llm`, `first_sentence`, `voice`, `hold`, `total`. The same numbers as the `[turn-timing]` log line |
| `approx` | The caller's voice ending wasn't measured, so `total` starts at the transcript and is left out of reply times |
| `spoke`, `cut`, `cut_after_ms` | Whether the caller heard the reply, and whether (and after how long) they cut into it |
| `superseded` | The caller went on talking before the reply played, and their next words replaced it. Not a missed reply |
| `agent_error` | Your agent failed or timed out |
| `signals` | Signs of trouble, below |

**Per call** (`"type": "call"`): transport (`webrtc` or `phone`), start and
end, `ended_by` (`caller`, `agent` or `error`), turns, languages, voices used,
reply time p50/p95, signal counts, and the outcome your app reported.

**Never recorded:** audio, transcripts, or anything the caller or agent said.
The caller's words are used in memory to notice a repeat, then dropped. That's
why reports are on by default.

## Signals

Each one can be computed without anyone listening to the call, and each
points at a cause.

| Signal | When | Usually means |
|---|---|---|
| `slow` | First audio more than 3 s after the caller stopped | Latency: look at `ms` to see which stage |
| `silent` | The caller got no reply at all | The agent failed, or every voice did |
| `repeat` | The caller says nearly the same thing again | Misheard, or a wrong answer |
| `check_in` | "Hello? Are you there?" after the first turn | Dead air |
| `cut_early` | The caller cut in within 1.5 s of the reply starting | A false barge-in, or a reply they knew was wrong |
| `lang_flip` | The language went A → B → A | The switch to B was wrong |
| `low_confidence` | Speech-to-text confidence under 0.6 | A bad line, or the wrong listener |
| `hung_up_mid_reply` (per call) | The caller hung up while the agent was talking | Gave up |

The headline, **answered turns with no sign of trouble**, is the number to
watch per language. Next to it, watch p95 reply time, and success if your app
reports outcomes.

## Reporting how the call went

Your agent can add an `outcome` to any `chat` reply (see the
[agent contract](agent-contract.md#chat)):

```json
{"text": "You're booked for eight.", "outcome": {"success": true, "reason": "booked"}}
```

The last one in a call is kept, and `voxie report` shows how many calls
succeeded.

## Using it

- **After a change**, compare this week's report with last week's, per language.
- **When a number is bad**, look at that language's `turn` lines: `ms` shows
  which stage is slow, `voice` and `listener` show which provider, and
  `signals` show what the caller did.
- **Every real failure you find** is worth turning into a scripted caller
  (`tools/callers/`), so it can't come back unnoticed.

The files are plain JSON lines, so `jq`, DuckDB or a spreadsheet can answer
anything the summary doesn't.
