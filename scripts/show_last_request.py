import json, os
d=json.load(open(os.path.join(os.path.expanduser('~'), 'dev/freeride/last_payload.json')))
for i,m in enumerate(d['messages']):
    print('===', i, m['role'], 'len', len(m['content']), '===')
    print(m['content'])

