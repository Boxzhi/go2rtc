package mp4

import (
 "testing"
 "time"
)

func resetLuxeTasks(t *testing.T) {
 t.Helper()
 luxeRecordManager.Lock()
 old:=luxeRecordManager.tasks
 luxeRecordManager.tasks=make(map[string]*luxeRecordTask)
 luxeRecordManager.Unlock()
 t.Cleanup(func(){luxeRecordManager.Lock();luxeRecordManager.tasks=old;luxeRecordManager.Unlock()})
}
func testTask(src,id string)*luxeRecordTask{
 return &luxeRecordTask{ID:id, Src:src, token:"test-token", StartedAt:time.Now(), Status:"starting",stopCh:make(chan struct{})}
}
func TestLuxeMaxThreeIndependentStreams(t *testing.T){
 resetLuxeTasks(t)
 for i,src:=range []string{"ptz","fixed","canting"} {
  if err:=luxeRegister(testTask(src,string(rune('a'+i))));err!=nil{t.Fatal(err)}
 }
 if err:=luxeRegister(testTask("fourth","d"));err==nil{t.Fatal("expected capacity limit")}
}
func TestLuxeDuplicateStreamAndID(t *testing.T){
 resetLuxeTasks(t)
 first:=testTask("ptz","id1")
 if err:=luxeRegister(first);err!=nil{t.Fatal(err)}
 if err:=luxeRegister(testTask("ptz","id2"));err==nil{t.Fatal("expected duplicate stream refusal")}
 if err:=luxeRegister(testTask("fixed","id1"));err==nil{t.Fatal("expected duplicate id refusal")}
 luxeFinish(first,"completed","")
 if err:=luxeRegister(testTask("ptz","id3"));err!=nil{t.Fatalf("expected stream to be released: %v",err)}
}
func TestLuxeStatusAfterFinish(t *testing.T){
 resetLuxeTasks(t)
 task:=testTask("ptz","id1")
 if err:=luxeRegister(task);err!=nil{t.Fatal(err)}
 luxeSetStatus(task,"recording")
 if s:=luxeSnapshot(task);s.Status!="recording"{t.Fatalf("unexpected status: %s",s.Status)}
 luxeFinish(task,"completed","")
 luxeSetStatus(task,"stopping")
 if s:=luxeSnapshot(task);s.Status!="completed"||s.EndedAt==nil{t.Fatal("completed task changed state")}
}
