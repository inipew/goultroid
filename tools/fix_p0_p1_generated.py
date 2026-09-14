from pathlib import Path

p = Path("internal/scheduler/engine.go")
text = p.read_text()
old = "\t\tclaimBatch = 0\n\t\tfor len(reservations) < 10 {"
new = "\t\tfor len(reservations) < 10 {"
if text.count(old) != 1:
    raise RuntimeError(f"expected one dead claimBatch assignment, found {text.count(old)}")
p.write_text(text.replace(old, new, 1))
