package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestPanelSendKeepsUnsentText(t *testing.T) {
	source, err := os.ReadFile("pages.go")
	if err != nil {
		t.Fatal(err)
	}
	script := string(source)
	start := strings.Index(script, "async function sendMsg(e) {")
	if start < 0 {
		t.Fatal("sendMsg script missing")
	}
	end := strings.Index(script[start:], "\n</script>")
	if end < 0 {
		t.Fatal("sendMsg script missing")
	}
	script = script[start:start+end] + `
const assert = require('assert');
const input = {value:'first'};
const error = {textContent:''};
const document = {getElementById: id => id === 'msg-input' ? input : error};
const folder = 'test', _topic = 'topic';
let fetch = async () => ({ok:false});
(async () => {
 await sendMsg({preventDefault(){}});
 assert.equal(input.value, 'first'); assert(error.textContent);
 error.textContent = '';
 fetch = async () => { throw Error('offline') };
 await sendMsg({preventDefault(){}});
 assert.equal(input.value, 'first'); assert(error.textContent);
 let finish;
 fetch = () => new Promise(resolve => {finish=resolve});
 const pending = sendMsg({preventDefault(){}});
 input.value = 'second'; finish({ok:true}); await pending;
 assert.equal(input.value, 'second');
 input.value = 'third'; fetch = async () => ({ok:true});
 await sendMsg({preventDefault(){}}); assert.equal(input.value, '');
})().catch(e => { console.error(e); process.exit(1) });`
	cmd := exec.Command("node", "-e", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
}
