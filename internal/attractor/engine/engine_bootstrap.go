package engine

import (
	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

func newBaseEngine(g *model.Graph, dotSource []byte, opts RunOptions) *Engine {
	e := &Engine{
		Graph:       g,
		Options:     opts,
		DotSource:   append([]byte{}, dotSource...),
		LogsRoot:    opts.LogsRoot,
		WorktreeDir: opts.WorktreeDir,
		Context:     runtime.NewContext(),
		Registry:    NewDefaultRegistry(),
		Interviewer: &AutoApproveInterviewer{},
		Artifacts:   NewArtifactStore(opts.LogsRoot, DefaultFileBackingThreshold),
	}
	if opts.ProgressSink != nil {
		e.progressSink = opts.ProgressSink
	}
	if opts.Interviewer != nil {
		e.Interviewer = opts.Interviewer
	}
	e.RunBranch = buildRunBranch(opts.RunBranchPrefix, opts.RunID)
	return e
}
