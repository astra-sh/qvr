import { useState } from "react";
import { Route, Routes } from "react-router-dom";
import Layout from "./components/Layout";
import Overview from "./pages/Overview";
import Sessions from "./pages/Sessions";
import SessionDetail from "./pages/SessionDetail";
import Skills from "./pages/Skills";
import SkillDetail from "./pages/SkillDetail";
import Tree from "./pages/Tree";
import Scan from "./pages/Scan";
import Provenance from "./pages/Provenance";
import Registries from "./pages/Registries";
import { getScope, setScope, scopeToken, type Scope } from "./api";

export default function App() {
  // The active project/global scope lives here. api.ts owns persistence; we
  // mirror it into state and remount the routed pages on change (keyed by the
  // scope token) so every loader re-runs against the newly selected project.
  const [scope, setScopeState] = useState<Scope>(() => getScope());

  function changeScope(s: Scope) {
    setScope(s);
    setScopeState(s);
  }

  return (
    <Layout scope={scope} onScopeChange={changeScope}>
      <Routes key={scopeToken(scope)}>
        <Route path="/" element={<Overview />} />
        <Route path="/registries" element={<Registries />} />
        <Route path="/sessions" element={<Sessions />} />
        <Route path="/sessions/:id" element={<SessionDetail />} />
        <Route path="/skills" element={<Skills />} />
        <Route path="/skills/:name" element={<SkillDetail />} />
        <Route path="/tree" element={<Tree />} />
        <Route path="/scan" element={<Scan />} />
        <Route path="/provenance" element={<Provenance />} />
        <Route path="*" element={<Overview />} />
      </Routes>
    </Layout>
  );
}
