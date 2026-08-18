# backlogr

Draws an unplayed game from your Steam library and sticks with it until you play or skip it.

## Install

```
go install github.com/tsraveling/backlogr@latest
```

First run opens Steam to mint a free Web API key, then logs you in.

## Flags

| Flag | Does |
| --- | --- |
| `-l`, `--list` | List every matching game instead of drawing one. |
| `-t`, `--threshold 2.5` | Count games under this many hours as unplayed. Default zero. |
| `-f`, `--format plain` | List format: `plain`, `md-checklist`, `csv`, `tsv`. Needs `--list`. |
| `--drawdeck DECK.md` | Write matching games to a markdown deck. Keeps checked items. |
| `-s`, `--sequential` | Pick alphabetically instead of at random. |
| `--skip` | Skip the current game and draw another. |
| `--reset` | Clear all skips. |
| `--login` | Force a fresh Steam login. |
| `--key` | Enter a new Steam API key. |
