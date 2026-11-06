import { useEffect, useState } from "react";
import { api, type AgentDefinition, type Skill } from "./api";
import { reviews, roleOf, ROLES } from "./agentRoles";

export function AgentLink({ name }: { name: string }) {
  return <a className="agent-link" href={`#/team/${name}`} onClick={(e) => e.stopPropagation()}><code>{name}</code></a>;
}

const names = (list: string[] | undefined) =>
  list?.length ? list.map((n, i) => <span key={n}>{i > 0 && ", "}<AgentLink name={n} /></span>) : "—";

/** The agents, by the part each plays and who decides it runs. */
export function Team({ name, canReload }: { name?: string; canReload: boolean }) {
  const [agents, setAgents] = useState<AgentDefinition[] | null>(null);
  const [err, setErr] = useState("");
  const [reloading, setReloading] = useState(false);

  const load = () => api.listAgents().then(setAgents).catch((e) => setErr(String(e)));
  useEffect(() => { load(); }, []);

  const reload = async () => {
    setReloading(true);
    try { await api.reloadAgents(); await load(); setErr(""); }
    catch (e) { setErr(String(e)); }
    finally { setReloading(false); }
  };

  if (err && !agents) return <p className="error">{err}</p>;
  if (!agents) return <p className="hint">Loading…</p>;
  if (name) return <AgentPage agent={agents.find((a) => a.name === name)} name={name} agents={agents} />;

  return (
    <div className="team">
      <div className="team-head">
        <h2>Team</h2>
        {canReload && <button onClick={reload} disabled={reloading} title="Pick up changes to agents/ and skills/">{reloading ? "Reloading…" : "Reload from disk"}</button>}
      </div>
      {err && <p className="error">{err}</p>}
      <p>
        Nobody assigns work by hand. The head engineer reads the task and the repository, then picks an agent for each
        subtask from the descriptions below, so a description is that agent's job advert. Forge refuses a plan that names
        an unknown agent or one that doesn't do the work. It doesn't judge whether the choice was a good one.
      </p>
      {ROLES.map((role) => {
        const members = agents.filter((a) => roleOf(a) === role.id);
        if (members.length === 0) return null;
        return (
          <section key={role.id}>
            <h3>{role.title}</h3>
            <p className="hint">{role.how}</p>
            <div className="table-scroll">
              <table>
                <thead><tr><th>Agent</th><th>Job</th><th>Skills</th><th>{role.id === "reviews" ? "Reviews work by" : "Reviewed by"}</th></tr></thead>
                <tbody>
                  {members.map((a) => (
                    <tr key={a.name}>
                      <td><AgentLink name={a.name} /></td>
                      <td>{a.description}</td>
                      <td>{a.skills?.join(", ") || "—"}</td>
                      <td>{names(role.id === "reviews" ? reviews(a.name, agents) : a.reviewers)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </section>
        );
      })}
    </div>
  );
}

function AgentPage({ agent, name, agents }: { agent?: AgentDefinition; name: string; agents: AgentDefinition[] }) {
  const [skills, setSkills] = useState<Skill[]>([]);
  useEffect(() => { api.listSkills().then(setSkills).catch(() => setSkills([])); }, []);

  if (!agent) {
    return <div className="detail"><a className="back" href="#/team">← Team</a><p>There is no agent called <code>{name}</code> now.</p></div>;
  }
  const role = ROLES.find((r) => r.id === roleOf(agent))!;
  const reviewed = reviews(agent.name, agents);
  return (
    <div className="detail team-agent">
      <a className="back" href="#/team">← Team</a>
      <h2>{agent.name} <span className="version">v{agent.version}</span></h2>
      <p>{agent.description}</p>
      <section className="assignment">
        <h3>{role.title}</h3>
        <p>{role.how}</p>
        <dl>
          <dt>Reviewed by</dt><dd>{agent.reviewers?.length ? names(agent.reviewers) : "No agent"}</dd>
          {reviewed.length > 0 && <><dt>Reviews work by</dt><dd>{names(reviewed)}</dd></>}
          {agent.modelPreference && <><dt>Model preference</dt><dd>{agent.modelPreference}</dd></>}
        </dl>
      </section>
      <section>
        <h3>Tools</h3>
        <p>{agent.tools.map((t, i) => <span key={t}>{i > 0 && ", "}<code>{t}</code></span>)}</p>
      </section>
      <section>
        <h3>Skills</h3>
        {!agent.skills?.length ? <p>—</p> : agent.skills.map((n) => {
          const s = skills.find((x) => x.name === n);
          return (
            <details key={n}>
              <summary><strong>{n}</strong>{s?.description ? `: ${s.description}` : ""}</summary>
              {s && <pre className="skill-body">{s.content}</pre>}
            </details>
          );
        })}
      </section>
      <section>
        <h3>System prompt</h3>
        <pre className="system-prompt">{agent.systemPrompt}</pre>
      </section>
    </div>
  );
}
