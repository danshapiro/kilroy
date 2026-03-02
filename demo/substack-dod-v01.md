# Substack Creator Newsletter Engine — Definition of Done

## Scope

### In Scope
- Pure React frontend application (no backend)
- Configuration wizard (API key → company → voice → guardrails)
- Dashboard with new post, trending topics, and post history
- Trending Topics with search-grounded research and topic synthesis
- New Post pipeline: Topic → Research → Outline → Write (3-cycle) → Complete
- Rich input control component (text, documents, links)
- Demo mode with session replay using production code path
- Production mode with full session recording to IndexedDB
- Pre-recorded P&G demo session shipped with the app
- LLM integration: Gemini 3.1 Flash, 3.1 Pro (extended thinking), 2.5 Flash Lite (test)
- Structured output mode with retry/backoff for all LLM calls
- Substack-like visual design (fonts, colors, cards, step indicators, progress bars)
- Test suite: integration (canned data), smoke (Flash Lite), manual (real models)
- Railway deployment from private GitHub repo

### Out of Scope
- Backend services or server-side logic
- RAG (explicitly excluded by spec)
- Substack API integration or direct publishing
- User authentication or multi-user support
- Non-public company data (runtime-only user input)
- Mobile-specific responsive layouts (not specified)

### Assumptions
- User has a valid Google Gemini API key
- Modern browser with IndexedDB support
- Private GitHub repo with Railway deployment configured
- Node.js/npm toolchain for build

## Deliverables

| Artifact | Location | Description |
|----------|----------|-------------|
| React Application | `src/` + build output | Complete frontend app with all flows |
| Pre-recorded Demo Session | Bundled in app | P&G post session data for out-of-box demo mode |
| Test Suite | `src/**/*.test.*` or `tests/` | Integration, smoke, and manual test configurations |
| Railway Config | repo root | Deployment configuration for Railway |

## Acceptance Criteria

### 1. Build & Deployment

| ID | Criterion | Covered by |
|----|-----------|------------|
| AC-1.1 | `npm run build` exits 0 with no errors | IT-1 |
| AC-1.2 | Built app serves and loads in a browser without console errors | IT-1, IT-2 |
| AC-1.3 | Railway deployment config exists and references the build output | IT-1 |

### 2. API Key & Persistence

| ID | Criterion | Covered by |
|----|-----------|------------|
| AC-2.1 | First run displays API key entry screen | IT-3 |
| AC-2.2 | Entered API key persists in IndexedDB across page reloads | IT-3 |
| AC-2.3 | Subsequent loads skip API key entry when key exists in IndexedDB | IT-3 |

### 3. Configuration Wizard

| ID | Criterion | Covered by |
|----|-----------|------------|
| AC-3.1 | Wizard flows through 6 steps: identify company → confirm → define voice → confirm → define guardrails → confirm | IT-3 |
| AC-3.2 | Each input step (1, 3, 5) uses the rich input control component | IT-3, IT-8 |
| AC-3.3 | Each confirmation step (2, 4, 6) displays LLM-generated summary (one paragraph + detailed instructions) | IT-3 |
| AC-3.4 | Back button on each confirmation step returns to prior input step | IT-3 |
| AC-3.5 | Completed configuration persists in IndexedDB | IT-3 |
| AC-3.6 | Completing step 6 confirmation navigates to dashboard | IT-3 |

### 4. Dashboard

| ID | Criterion | Covered by |
|----|-----------|------------|
| AC-4.1 | "New Post" button is prominently displayed and navigates to New Post flow | IT-4 |
| AC-4.2 | "Trending Topics" button is prominently displayed and navigates to Trending Topics | IT-5 |
| AC-4.3 | Past posts and drafts are listed on the dashboard | IT-4, IT-6 |

### 5. Trending Topics

| ID | Criterion | Covered by |
|----|-----------|------------|
| AC-5.1 | Opening Trending Topics triggers parallel Gemini 3.1 Flash search-grounded calls | IT-5 |
| AC-5.2 | Results build a visualization of topics and trends deterministically as they return | IT-5 |
| AC-5.3 | Gemini 3.1 Pro synthesizes sources into exactly 3 writing prompts | IT-5 |
| AC-5.4 | Selecting a prompt navigates to New Post with topic prefilled | IT-5 |

### 6. New Post Pipeline

| ID | Criterion | Covered by |
|----|-----------|------------|
| AC-6.1 | Topic step displays rich input control and a "Research" button | IT-4 |
| AC-6.2 | Research step displays sources with headline and snippet as each returns | IT-4 |
| AC-6.3 | Each source carries structured metadata: URL, title, author, publication date | IT-4 |
| AC-6.4 | User can highlight interesting sources | IT-4 |
| AC-6.5 | User can delete unwanted sources | IT-4 |
| AC-6.6 | Outline step produces a one-shot outline via Gemini 3.1 Pro from all gathered material | IT-4 |
| AC-6.7 | Outline step provides accept and go-back options | IT-4 |
| AC-6.8 | Write step executes a visible 3-cycle process: Write → Edit → Guardrails | IT-4 |
| AC-6.9 | Write cycle 1 produces article without guardrails, with inline citations referencing research sources | IT-4 |
| AC-6.10 | Write cycle 2 revises for style guide alignment | IT-4 |
| AC-6.11 | Write cycle 3 receives only the guardrails document and the post (nothing else) | IT-4 |
| AC-6.12 | Complete step displays formatted post with proper footnotes — each citation rendered with source title and link | IT-4, IT-9 |
| AC-6.13 | Completing a post returns to dashboard | IT-4 |

### 7. Rich Input Control

| ID | Criterion | Covered by |
|----|-----------|------------|
| AC-7.1 | Rich input control is a single reusable component used across all input surfaces | IT-8 |
| AC-7.2 | Accepts typed and pasted text | IT-8, IT-3 |
| AC-7.3 | Accepts uploaded documents | IT-8 |
| AC-7.4 | Accepts links | IT-8 |
| AC-7.5 | Renders as textarea with attach and link toolbar at the bottom | IT-8, IT-9 |

### 8. Demo Mode

| ID | Criterion | Covered by |
|----|-----------|------------|
| AC-8.1 | Demo mode presents a list of all recorded sessions to choose from | IT-2, IT-6 |
| AC-8.2 | Selected session replays using the production code path (not a separate replay engine) | IT-2 |
| AC-8.3 | Text fields prefill instantly with a subtle fade-in animation | IT-2 |
| AC-8.4 | Attachments appear a moment after page load | IT-2 |
| AC-8.5 | The button that was clicked next becomes highlighted before user clicks | IT-2 |
| AC-8.6 | App ships with one pre-recorded P&G post session | IT-2 |
| AC-8.7 | Demo mode works out of the box with no configuration required | IT-2 |

### 9. Session Recording

| ID | Criterion | Covered by |
|----|-----------|------------|
| AC-9.1 | Every production session records all inputs, all LLM responses, and all intermediate state | IT-6 |
| AC-9.2 | Recorded sessions are stored in IndexedDB | IT-6 |
| AC-9.3 | Recorded sessions appear in demo mode session list for replay | IT-6 |

### 10. LLM Integration

| ID | Criterion | Covered by |
|----|-----------|------------|
| AC-10.1 | All LLM replies use structured output mode (JSON enforced to schema) | IT-7 |
| AC-10.2 | Failed structured output calls retry with the incorrect answer and failure reason as feedback | IT-7 |
| AC-10.3 | Retry uses intelligent backoff | IT-7 |
| AC-10.4 | LLM calls are made client-side using the stored user API key | IT-3, IT-4 |
| AC-10.5 | Gemini 3.1 Flash used for research and fast tasks | IT-4, IT-5 |
| AC-10.6 | Gemini 3.1 Pro with extended thinking used for confirmations, outlines, synthesis, and writing | IT-3, IT-4, IT-5 |
| AC-10.7 | Test mode substitutes Gemini 2.5 Flash Lite for all models | IT-10 |

### 11. Visual Design

| ID | Criterion | Covered by |
|----|-----------|------------|
| AC-11.1 | Visual styling (font family, colors) resembles Substack | IT-9 |
| AC-11.2 | Multi-step flows show horizontal dots as step indicators: accent = current, green = done, outlined = future | IT-9, IT-2 |
| AC-11.3 | Cards used as containers for content blocks, source results, confirmations, and previews | IT-9 |
| AC-11.4 | Progress shown as horizontal bars with accent color fill animating left-to-right | IT-9 |
| AC-11.5 | No spinners or skeleton loaders exist anywhere in the app | IT-9 |
| AC-11.6 | Final post preview uses serif font with numbered footnotes and linked sources | IT-9, IT-4 |
| AC-11.7 | Post preview visually resembles a real Substack post | IT-9 |

### 12. Testing

| ID | Criterion | Covered by |
|----|-----------|------------|
| AC-12.1 | Integration tests run with canned/hardcoded LLM data and exit 0 | IT-10 |
| AC-12.2 | Smoke tests use live Gemini 2.5 Flash Lite calls and exit 0 | IT-10 |
| AC-12.3 | Smoke tests execute only after integration tests pass | IT-10 |
| AC-12.4 | Manual test option runs with real LLM configuration (3.1 Flash + 3.1 Pro) | IT-10 |

## User-Facing Message Inventory

| ID | Message surface | Trigger condition | Covered by |
|----|----------------|-------------------|------------|
| MSG-1 | API key entry screen | First run or no key in IndexedDB | IT-3 |
| MSG-2 | "Identify your company" input prompt | Config wizard step 1 | IT-3, IT-2 |
| MSG-3 | Company confirmation: one-paragraph summary + detailed instructions | Config wizard step 2 (LLM response) | IT-3, IT-2 |
| MSG-4 | Back button on company confirmation | Config wizard step 2 | IT-3 |
| MSG-5 | "Define voice" input prompt | Config wizard step 3 | IT-3, IT-2 |
| MSG-6 | Voice confirmation summary | Config wizard step 4 (LLM response) | IT-3, IT-2 |
| MSG-7 | Back button on voice confirmation | Config wizard step 4 | IT-3 |
| MSG-8 | "Define guardrails" input prompt | Config wizard step 5 | IT-3, IT-2 |
| MSG-9 | Guardrails confirmation summary | Config wizard step 6 (LLM response) | IT-3, IT-2 |
| MSG-10 | Back button on guardrails confirmation | Config wizard step 6 | IT-3 |
| MSG-11 | Dashboard "New Post" button | Dashboard loaded | IT-4, IT-2 |
| MSG-12 | Dashboard "Trending Topics" button | Dashboard loaded | IT-5, IT-2 |
| MSG-13 | Dashboard past posts/drafts listing | Dashboard loaded with history | IT-4, IT-6 |
| MSG-14 | Trending Topics research progress (results building as calls return) | Trending Topics opened | IT-5 |
| MSG-15 | Trending Topics: topic/trend visualization | Research calls complete | IT-5 |
| MSG-16 | Trending Topics: 3 synthesized writing prompts | Pro synthesis complete | IT-5 |
| MSG-17 | New Post Topic: rich input control | New Post opened | IT-4 |
| MSG-18 | New Post Topic: "Research" button | New Post topic step | IT-4 |
| MSG-19 | New Post Research: source headline + snippet (per result) | Research calls returning | IT-4 |
| MSG-20 | New Post Research: source structured metadata (URL, title, author, date) | Source rendered | IT-4 |
| MSG-21 | New Post Research: highlight control on each source | Sources displayed | IT-4 |
| MSG-22 | New Post Research: delete control on each source | Sources displayed | IT-4 |
| MSG-23 | New Post Outline: generated outline display | Outline step reached | IT-4 |
| MSG-24 | New Post Outline: accept button | Outline displayed | IT-4 |
| MSG-25 | New Post Outline: go-back button | Outline displayed | IT-4 |
| MSG-26 | New Post Write: cycle 1 "Write" progress indicator | Write step begins | IT-4 |
| MSG-27 | New Post Write: cycle 2 "Edit" progress indicator | Cycle 1 completes | IT-4 |
| MSG-28 | New Post Write: cycle 3 "Guardrails" progress indicator | Cycle 2 completes | IT-4 |
| MSG-29 | New Post Complete: formatted post with numbered footnotes (source title + link) | All write cycles complete | IT-4, IT-9 |
| MSG-30 | Demo mode: session selection list | Demo mode entered | IT-2, IT-6 |
| MSG-31 | Demo mode: prefilled text fields with fade-in animation | Demo session replaying | IT-2 |
| MSG-32 | Demo mode: highlighted next button | Demo session at action point | IT-2 |
| MSG-33 | Demo mode: attachments appearing after page load | Demo session with attachments | IT-2 |
| MSG-34 | Step indicators: horizontal dots (accent = current, green = done, outlined = future) | Any multi-step flow | IT-9, IT-2, IT-4 |
| MSG-35 | Progress bars: horizontal accent fill animating left-to-right | Any loading/progress state | IT-9, IT-4 |
| MSG-36 | Card containers for content blocks, sources, confirmations, previews | Content rendered throughout app | IT-9 |
| MSG-37 | Rich input control: textarea with attach toolbar | Any input step | IT-8, IT-3 |
| MSG-38 | Rich input control: link toolbar | Any input step | IT-8, IT-3 |

## Integration Test Scenarios

### IT-1: App Builds and Loads in Browser

Proves the deliverable works in its delivery form.

| Step | Action | Expected |
|------|--------|----------|
| 1 | Run `npm run build` | Exits 0, no errors |
| 2 | Serve the built app on localhost | Dev server starts |
| 3 | Open app in headless browser | Page loads, no console errors |
| 4 | Check for Railway deployment config in repo | Config file exists and references build output |

**Covers:** AC-1.1, AC-1.2, AC-1.3
**Verification:** `npm test -- --grep "IT-1"` exits 0

---

### IT-2: Demo Mode Out-of-Box Replay (P&G Session)

Exercises demo mode end-to-end with no prior setup, proving the shipped P&G session replays correctly through the production code path.

| Step | Action | Expected |
|------|--------|----------|
| 1 | Load app in browser with clean IndexedDB | App loads |
| 2 | Enter demo mode | Session selection list displays with at least one P&G session |
| 3 | Select the P&G session | Replay begins at configuration step 1 |
| 4 | Observe config step 1 | "Identify your company" text field prefills with fade-in animation |
| 5 | Observe next button | Button that was clicked next becomes highlighted |
| 6 | Click highlighted button | Advances to config step 2: company confirmation displays summary |
| 7 | Step indicator | Shows step 1 green (done), step 2 accent (current), remaining outlined |
| 8 | Click through remaining config steps (3–6) | Each input prefills with fade-in; each confirmation displays summary; step indicators update |
| 9 | Config completes → dashboard | Dashboard displays with "New Post" and "Trending Topics" buttons |
| 10 | Continue replay through New Post flow | Topic prefills, research sources appear, outline displays, write cycles progress with horizontal bars |
| 11 | Write cycles | Progress bars show accent fill left-to-right; 3 cycles visible (Write → Edit → Guardrails) |
| 12 | Complete step | Formatted post displays in serif font with numbered footnotes and linked sources |
| 13 | Verify no spinners or skeletons appeared during entire replay | No spinner/skeleton elements in DOM at any point |
| 14 | Verify card containers used for content blocks | Sources, confirmations, and previews render inside card primitives |

**Covers:** AC-1.2, AC-8.1, AC-8.2, AC-8.3, AC-8.4, AC-8.5, AC-8.6, AC-8.7, AC-11.2, AC-11.3, AC-11.4, AC-11.5, AC-11.6
**Messages:** MSG-2, MSG-3, MSG-5, MSG-6, MSG-8, MSG-9, MSG-11, MSG-12, MSG-30, MSG-31, MSG-32, MSG-33, MSG-34, MSG-35, MSG-36
**Verification:** `npm test -- --grep "IT-2"` exits 0

---

### IT-3: Configuration Wizard Full Flow

Exercises the complete first-run experience with mocked LLM responses, verifying all 6 steps, back navigation, persistence, and IndexedDB storage.

| Step | Action | Expected |
|------|--------|----------|
| 1 | Load app with clean IndexedDB | API key entry screen displays |
| 2 | Enter API key and submit | Key stored in IndexedDB; wizard step 1 begins |
| 3 | Reload page | App skips API key entry, resumes at wizard (key found in IndexedDB) |
| 4 | Step 1: Enter company info via rich input control | Rich input renders as textarea with attach + link toolbar; accepts text input |
| 5 | Submit step 1 | Step 2 displays: one-paragraph summary + beautifully formatted detailed instructions |
| 6 | Click back button on step 2 | Returns to step 1 with prior input preserved |
| 7 | Re-submit step 1 | Step 2 displays again with confirmation |
| 8 | Proceed from step 2 | Step 3: "Define voice" input with rich input control |
| 9 | Enter voice description, submit | Step 4: voice confirmation summary displays |
| 10 | Click back button on step 4 | Returns to step 3 |
| 11 | Re-submit, proceed from step 4 | Step 5: "Define guardrails" input |
| 12 | Enter guardrails, submit | Step 6: guardrails confirmation summary displays |
| 13 | Click back button on step 6 | Returns to step 5 |
| 14 | Re-submit, proceed from step 6 | Navigates to dashboard |
| 15 | Verify IndexedDB contains configuration | Company, voice, guardrails data persisted |
| 16 | Verify step indicators throughout | Current step accent, completed steps green, future steps outlined |

**Covers:** AC-2.1, AC-2.2, AC-2.3, AC-3.1, AC-3.2, AC-3.3, AC-3.4, AC-3.5, AC-3.6, AC-7.2, AC-7.5, AC-10.4, AC-10.6
**Messages:** MSG-1, MSG-2, MSG-3, MSG-4, MSG-5, MSG-6, MSG-7, MSG-8, MSG-9, MSG-10, MSG-34, MSG-37, MSG-38
**Verification:** `npm test -- --grep "IT-3"` exits 0

---

### IT-4: New Post Full Pipeline

Exercises the complete post creation flow from dashboard through all 5 stages, verifying research, outlining, the 3-cycle write process, and final formatted output.

| Step | Action | Expected |
|------|--------|----------|
| 1 | Load app with existing configuration in IndexedDB | Dashboard displays |
| 2 | Click "New Post" button | Topic step: rich input control + "Research" button visible |
| 3 | Enter topic text via rich input | Text accepted in textarea |
| 4 | Click "Research" | Research step begins; sources appear incrementally with headline + snippet |
| 5 | Verify source metadata | Each source carries URL, title, author, publication date |
| 6 | Highlight one source | Source visually marked as highlighted |
| 7 | Delete another source | Source removed from list |
| 8 | Proceed to next step | Outline step: generated outline displayed |
| 9 | Click go-back | Returns to research step with prior selections preserved |
| 10 | Proceed again, click accept on outline | Write step begins |
| 11 | Observe write cycle 1 "Write" | Progress indicator shows "Write" active; horizontal bar animates |
| 12 | Observe write cycle 2 "Edit" | Progress indicator shows "Edit" active |
| 13 | Observe write cycle 3 "Guardrails" | Progress indicator shows "Guardrails" active |
| 14 | All cycles complete → Complete step | Formatted post displays |
| 15 | Verify post format | Serif font; numbered footnotes; each citation has source title + link |
| 16 | Verify inline citations reference research sources | Citations map to sources from step 4 (highlighted sources included, deleted excluded) |
| 17 | Click return to dashboard | Dashboard displays; new post appears in past posts/drafts list |
| 18 | Verify step indicators throughout flow | Correct accent/green/outlined states at each stage |
| 19 | Verify cards used for source results, outline, and post preview | Content renders inside card primitives |

**Covers:** AC-4.1, AC-4.3, AC-6.1, AC-6.2, AC-6.3, AC-6.4, AC-6.5, AC-6.6, AC-6.7, AC-6.8, AC-6.9, AC-6.10, AC-6.11, AC-6.12, AC-6.13, AC-10.4, AC-10.5, AC-10.6, AC-11.4, AC-11.6
**Messages:** MSG-11, MSG-13, MSG-17, MSG-18, MSG-19, MSG-20, MSG-21, MSG-22, MSG-23, MSG-24, MSG-25, MSG-26, MSG-27, MSG-28, MSG-29, MSG-34, MSG-35, MSG-36
**Verification:** `npm test -- --grep "IT-4"` exits 0

---

### IT-5: Trending Topics to New Post

Exercises the trending topics research flow and its handoff to the New Post pipeline.

| Step | Action | Expected |
|------|--------|----------|
| 1 | From dashboard, click "Trending Topics" | Trending Topics view opens |
| 2 | Observe research | Parallel search-grounded Flash calls fire; results build incrementally |
| 3 | Observe visualization | Topic/trend visualization renders deterministically as results arrive |
| 4 | Wait for synthesis | Gemini 3.1 Pro produces exactly 3 writing prompts |
| 5 | Verify 3 prompts displayed | Each prompt is a distinct, actionable topic suggestion |
| 6 | Select one prompt | Navigates to New Post flow |
| 7 | Verify topic field | Rich input control prefilled with the selected prompt text |
| 8 | Verify progress bar during research | Horizontal bar with accent fill, no spinners |

**Covers:** AC-4.2, AC-5.1, AC-5.2, AC-5.3, AC-5.4, AC-10.5, AC-10.6, AC-11.4
**Messages:** MSG-12, MSG-14, MSG-15, MSG-16, MSG-35
**Verification:** `npm test -- --grep "IT-5"` exits 0

---

### IT-6: Session Recording and Replay

Proves that production sessions are fully recorded and can be replayed through demo mode.

| Step | Action | Expected |
|------|--------|----------|
| 1 | Complete a full post creation session (using mocked LLM) | Session completes normally |
| 2 | Query IndexedDB for session data | Session record exists containing: all inputs, all LLM responses, all intermediate state |
| 3 | Verify session data completeness | Record includes config inputs, research results, outline, all 3 write cycle outputs |
| 4 | Enter demo mode | Session selection list displays |
| 5 | Verify newly recorded session appears in list | Session is selectable |
| 6 | Select the recorded session | Replay begins |
| 7 | Walk through replay | Text fields prefill with fade-in; buttons highlight; flow matches original session |
| 8 | Verify replay uses production code path | Same components render as in production (not a separate replay engine) |
| 9 | Complete replay | Post displays matching original session output |

**Covers:** AC-9.1, AC-9.2, AC-9.3, AC-8.1, AC-8.2, AC-8.3, AC-8.5
**Messages:** MSG-13, MSG-30, MSG-31, MSG-32
**Verification:** `npm test -- --grep "IT-6"` exits 0

---

### IT-7: Structured Output Retry and Backoff

Proves the LLM integration handles malformed responses correctly with retry logic.

| Step | Action | Expected |
|------|--------|----------|
| 1 | Configure mock LLM to return malformed JSON (not matching schema) on first call | Mock ready |
| 2 | Trigger an LLM call that uses structured output (e.g., company confirmation) | First call fails schema validation |
| 3 | Verify retry fires | Second call is made |
| 4 | Verify retry includes feedback | Second call prompt contains the malformed response and the reason for failure |
| 5 | Configure mock to return valid JSON on second call | Second call succeeds |
| 6 | Verify result renders correctly | Confirmation summary displays normally |
| 7 | Configure mock to fail 3 consecutive times with increasing delays | Backoff applies |
| 8 | Measure time between retries | Each retry waits longer than the previous (intelligent backoff) |

**Covers:** AC-10.1, AC-10.2, AC-10.3
**Verification:** `npm test -- --grep "IT-7"` exits 0

---

### IT-8: Rich Input Control Capabilities

Exercises all input modes of the reusable rich input component.

| Step | Action | Expected |
|------|--------|----------|
| 1 | Navigate to any input step (e.g., config step 1) | Rich input control renders |
| 2 | Verify component structure | Textarea with attach toolbar and link toolbar at bottom |
| 3 | Type text into textarea | Text appears in input |
| 4 | Paste text from clipboard | Pasted text appears in input |
| 5 | Click attach button, upload a document | Document appears as attachment indicator |
| 6 | Click link button, enter a URL | Link registered and displayed |
| 7 | Navigate to a different input step (e.g., config step 3) | Same rich input component renders |
| 8 | Verify same component | Identical structure and behavior (reusable, not duplicated) |
| 9 | Navigate to New Post topic step | Same rich input component renders |

**Covers:** AC-7.1, AC-7.2, AC-7.3, AC-7.4, AC-7.5
**Messages:** MSG-37, MSG-38
**Verification:** `npm test -- --grep "IT-8"` exits 0

---

### IT-9: Visual Design Audit

Semantic verification of visual design requirements against the Substack reference.

| Step | Action | Expected |
|------|--------|----------|
| 1 | Load app and navigate to dashboard | App renders |
| 2 | **Font family check**: inspect computed styles on body/main text | Font family matches Substack's style (likely a system serif/sans-serif stack) |
| 3 | **Color palette check**: inspect primary/accent colors | Colors are consistent with Substack's palette |
| 4 | Navigate through config wizard | Step indicator dots render: accent = current, green = done, outlined = future |
| 5 | Trigger a loading state (e.g., LLM call in progress) | Horizontal bar with accent fill animates left-to-right |
| 6 | Search entire DOM for spinner/skeleton elements | Zero spinners or skeleton loaders found |
| 7 | View source results in research step | Sources render inside card containers with clean spacing, no shadows |
| 8 | View confirmation in config wizard | Confirmation renders inside card container |
| 9 | View completed post in New Post flow | Post preview uses serif font |
| 10 | Verify footnotes | Numbered footnotes at bottom, each with source title and link |
| 11 | **Substack resemblance check** (semantic): screenshot the post preview | Visual comparison: post looks like it could appear on Substack |

**Semantic verification for steps 2–3 and 11:**
- Question: Does the app's visual design resemble Substack's look and feel?
- Expected: Yes — similar font choices, color palette, and content layout
- Evidence: Screenshots of app vs. substack.com; computed CSS values for font-family and primary colors

**Covers:** AC-11.1, AC-11.2, AC-11.3, AC-11.4, AC-11.5, AC-11.6, AC-11.7
**Messages:** MSG-29, MSG-34, MSG-35, MSG-36
**Verification:** `npm test -- --grep "IT-9"` exits 0

---

### IT-10: Test Matrix Execution

Proves the test infrastructure itself works: integration → smoke → manual.

| Step | Action | Expected |
|------|--------|----------|
| 1 | Run integration test suite | All tests pass using canned/hardcoded LLM data; exits 0 |
| 2 | Verify integration tests make zero live LLM calls | No network requests to Gemini API |
| 3 | Run smoke test suite | Tests execute using live Gemini 2.5 Flash Lite calls; exits 0 |
| 4 | Verify smoke tests substituted Flash Lite for all models | No calls to 3.1 Flash or 3.1 Pro endpoints |
| 5 | Verify smoke tests depend on integration tests | Running smoke tests alone first triggers integration tests (or smoke config requires prior integration pass) |
| 6 | Run manual test configuration | Tests use real 3.1 Flash + 3.1 Pro models |
| 7 | Verify nondeterminism handled | Structured output retry logic prevents crashes from Flash Lite nondeterminism in smoke tests |

**Covers:** AC-12.1, AC-12.2, AC-12.3, AC-12.4, AC-10.7
**Verification:** `npm run test:integration && npm run test:smoke` exits 0

---

## Crosscheck

### Per Scenario

| Scenario | Exercises delivered artifact? | Automatable? | Bounded? | Proportional? | Independent? | Crosses AC groups? |
|----------|------------------------------|--------------|----------|---------------|--------------|-------------------|
| IT-1 | Yes (build + browser load) | Yes | Yes (4 steps) | Yes | Yes | Build, Deployment |
| IT-2 | Yes (browser, production code path) | Yes | Yes (14 steps, deterministic replay) | Yes | Yes | Build, Demo, Visual, Rich Input |
| IT-3 | Yes (browser) | Yes | Yes (16 steps) | Yes | Yes | API Key, Config, Rich Input, LLM, Visual |
| IT-4 | Yes (browser) | Yes | Yes (19 steps) | Yes | Yes | Dashboard, New Post, LLM, Visual |
| IT-5 | Yes (browser) | Yes | Yes (8 steps) | Yes | Yes | Dashboard, Trending, LLM, Visual |
| IT-6 | Yes (browser + IndexedDB) | Yes | Yes (9 steps) | Yes | Yes | Session Recording, Demo Mode |
| IT-7 | Yes (LLM client) | Yes | Yes (8 steps) | Yes | Yes | LLM Integration |
| IT-8 | Yes (browser, component) | Yes | Yes (9 steps) | Yes | Yes | Rich Input |
| IT-9 | Yes (browser, visual) | Yes (except semantic screenshot comparison — flagged) | Yes (11 steps) | Yes | Yes | Visual Design |
| IT-10 | Yes (test runner) | Yes | Yes (7 steps) | Yes | Yes | Testing, LLM |

### Per AC — Coverage Confirmation

| AC Group | ACs | All covered? | Gaps |
|----------|-----|-------------|------|
| 1. Build & Deploy | AC-1.1–1.3 | Yes | — |
| 2. API Key | AC-2.1–2.3 | Yes | — |
| 3. Config Wizard | AC-3.1–3.6 | Yes | — |
| 4. Dashboard | AC-4.1–4.3 | Yes | — |
| 5. Trending Topics | AC-5.1–5.4 | Yes | — |
| 6. New Post | AC-6.1–6.13 | Yes | — |
| 7. Rich Input | AC-7.1–7.5 | Yes | — |
| 8. Demo Mode | AC-8.1–8.7 | Yes | — |
| 9. Session Recording | AC-9.1–9.3 | Yes | — |
| 10. LLM Integration | AC-10.1–10.7 | Yes | — |
| 11. Visual Design | AC-11.1–11.7 | Yes | AC-11.7 relies on semantic check |
| 12. Testing | AC-12.1–12.4 | Yes | — |

### Per Message — Coverage Confirmation

All 38 messages (MSG-1 through MSG-38) are covered by at least one integration test scenario. No gaps.

### Overall

- At least one scenario tests the deliverable in its delivery form (browser): IT-1, IT-2, IT-3, IT-4, IT-5, IT-6, IT-8, IT-9 ✓
- Every user-facing message is triggered and validated by at least one scenario ✓
- Scenarios collectively cover every AC group ✓
- One semantic verification flagged: AC-11.7 "resembles a real Substack post" requires visual judgment — addressed via screenshot comparison in IT-9 step 11 ✓
