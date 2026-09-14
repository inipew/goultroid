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

# The staged transform matches this block by text; normalize indentation only.
old = '''\t\t\t\t\tif repo != nil {\n\t\t\t\t\t\t_ = repo.UpdateState(context.Background(), jobID, StateCompleted, job.LastError, job.LastRun, job.NextRun)\n\t\t\t\t\t}\n\t\t\t\t\treturn nil'''
new = '''\t\t\t\tif repo != nil {\n\t\t\t\t\t_ = repo.UpdateState(context.Background(), jobID, StateCompleted, job.LastError, job.LastRun, job.NextRun)\n\t\t\t\t}\n\t\t\t\treturn nil'''
if text.count(old) != 1:
    raise RuntimeError(f'duplicate completion block normalization matched {text.count(old)} blocks')
text = text.replace(old, new, 1)

p.write_text(text)
