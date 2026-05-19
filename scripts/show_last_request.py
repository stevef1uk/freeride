import json
d=json.load(open('/home/stevef/dev/freeride/last_payload.json'))
for i,m in enumerate(d['messages']):
    print('===', i, m['role'], 'len', len(m['content']), '===')
    print(m['content'])

