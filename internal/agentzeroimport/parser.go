package agentzeroimport

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// Memory represents a single memory extracted from an Agent Zero pickle file.
type Memory struct {
	Content   string            `json:"content"`
	Source    string            `json:"source"`
	Tags      []string          `json:"tags"`
	Metadata  map[string]string `json:"metadata"`
	CreatedAt string            `json:"created_at,omitempty"`
}

// parserScript is the embedded Python script that safely unpickles Agent Zero
// FAISS index files and extracts memories as JSON.
const parserScript = `
import sys, json, pickle, io, collections

class StubDoc:
    def __init__(self, page_content="", metadata=None):
        self.page_content = page_content
        self.metadata = metadata or {}

class StubDocstore:
    def __init__(self, _dict=None):
        self._dict = _dict or {}

ALLOWED = {
    ("langchain_community.docstore.in_memory", "InMemoryDocstore"): StubDocstore,
    ("langchain.docstore.in_memory", "InMemoryDocstore"): StubDocstore,
    ("langchain_core.documents.base", "Document"): StubDoc,
    ("langchain.schema", "Document"): StubDoc,
    ("langchain.docstore.document", "Document"): StubDoc,
}

class SafeUnpickler(pickle.Unpickler):
    def find_class(self, module, name):
        key = (module, name)
        if key in ALLOWED:
            return ALLOWED[key]
        if module == "builtins" or module == "collections":
            return getattr(__builtins__ if module == "builtins" else collections, name)
        raise pickle.UnpicklingError(f"Blocked: {module}.{name}")

pkl_path = sys.argv[1]
areas = sys.argv[2].split(",") if len(sys.argv) > 2 and sys.argv[2] else []
include_knowledge = len(sys.argv) > 3 and sys.argv[3] == "1"

with open(pkl_path, "rb") as f:
    obj = SafeUnpickler(f).load()

docs = {}
if hasattr(obj, "_dict"):
    docs = obj._dict
elif isinstance(obj, tuple):
    for item in obj:
        if hasattr(item, "_dict"):
            docs = item._dict
            break
elif isinstance(obj, dict):
    for v in obj.values():
        if hasattr(v, "_dict"):
            docs = v._dict
            break

def get_doc_fields(doc):
    """Extract page_content and metadata from a doc, handling Pydantic v2 nesting."""
    d = doc.__dict__
    # Pydantic v2 nests real fields inside __dict__["__dict__"]
    if "__dict__" in d and isinstance(d["__dict__"], dict):
        d = d["__dict__"]
    return d.get("page_content", ""), d.get("metadata", {})

memories = []
for doc in docs.values():
    content, meta = get_doc_fields(doc)
    area = meta.get("area", "unknown")
    ks = meta.get("knowledge_source", "")

    if areas and area not in areas:
        continue
    if ks and not include_knowledge:
        continue

    ts = meta.get("timestamp", "")
    if ts and len(ts) == 19 and ts[4] == "-":
        ts = ts.replace(" ", "T") + "Z"

    tags = ["agent-zero", "agent-zero-" + area]
    m = {
        "content": content,
        "source": "agent-zero",
        "tags": tags,
        "metadata": {"area": area},
    }
    if ts:
        m["created_at"] = ts
    if ks:
        m["metadata"]["knowledge_source"] = str(ks)
    memories.append(m)

json.dump(memories, sys.stdout)
`

// ParsePickle shells out to python3 to safely unpickle an Agent Zero FAISS
// index file and returns the extracted memories.
func ParsePickle(pklPath string, areas []string, includeKnowledge bool) ([]Memory, error) {
	areasArg := strings.Join(areas, ",")
	knowledgeArg := "0"
	if includeKnowledge {
		knowledgeArg = "1"
	}

	cmd := exec.Command("python3", "-c", parserScript, pklPath, areasArg, knowledgeArg)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Check if python3 is available
		if execErr, ok := err.(*exec.Error); ok && execErr.Err == exec.ErrNotFound {
			return nil, fmt.Errorf("python3 is required but not found in PATH. Install Python 3 and try again")
		}
		return nil, fmt.Errorf("pickle parser failed: %w\n%s", err, string(out))
	}

	var memories []Memory
	if err := json.Unmarshal(out, &memories); err != nil {
		return nil, fmt.Errorf("parse pickle output: %w", err)
	}
	return memories, nil
}
