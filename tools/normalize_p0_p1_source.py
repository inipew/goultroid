from pathlib import Path
import re

p = Path('internal/jobs/manager.go')
text = p.read_text()
text, n = re.subn(
    r'\t\tterminalAt:\s+make\(map\[string\]time\.Time\),\n\t\tretention:\s+7 \* 24 \* time\.Hour,',
    '\t\tterminalAt:      make(map[string]time.Time),\n\t\tretention:        7 * 24 * time.Hour,',
    text,
    count=1,
)
if n != 1:
    raise RuntimeError(f'jobs manager initializer normalization matched {n} blocks')
p.write_text(text)
