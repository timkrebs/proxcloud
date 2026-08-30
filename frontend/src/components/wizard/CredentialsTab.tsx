"use client";
// Phase-C Credentials step (service mode only). For every credential the service
// declares, the user either GENERATES it (default — crypto/rand server-side,
// revealed once after create) or SETS it (supplies a password, and a username
// only when the credential is usernameSettable). Values live in WizardState;
// toProvisionRequest sends only the "set" ones. No secret is ever echoed back.
import { FormRow, SectionHeading } from "@/components/wizard/fields";
import { Input } from "@/components/ui/Input";
import { Mi } from "@/components/ui/icons";
import { useWizardStore, type WizardCredential, type WizardError } from "@/lib/stores/wizardStore";

// Strip the trailing " (Credentials)." tab hint so inline field errors read
// cleanly, matching the wizard's other tabs.
function fieldError(errs: WizardError[], field: string): string | undefined {
  return errs.find((e) => e.field === field)?.message.replace(/ \([A-Za-z+ ]+\)\.$/, ".");
}

function ModeRadio({
  cred,
  onMode,
}: {
  cred: WizardCredential;
  onMode: (mode: "generate" | "set") => void;
}) {
  const group = `cred-mode-${cred.name}`;
  const opt = (mode: "generate" | "set", title: string, desc: string) => {
    const checked = cred.mode === mode;
    const id = `${group}-${mode}`;
    return (
      <label
        htmlFor={id}
        className={`flex cursor-pointer items-start gap-[10px] rounded-fluent border p-3 ${
          checked ? "border-accent bg-selected" : "border-line-soft bg-card hover:border-accent"
        }`}
      >
        <input
          id={id}
          type="radio"
          name={group}
          checked={checked}
          onChange={() => onMode(mode)}
          className="mt-[2px] h-4 w-4 accent-[var(--color-accent)]"
        />
        <span>
          <span className="block text-[14px] font-semibold">{title}</span>
          <span className="mt-[2px] block text-[12px] text-ink-2">{desc}</span>
        </span>
      </label>
    );
  };
  return (
    <fieldset className="border-0 p-0" role="radiogroup" aria-label={`How to set ${cred.name}`}>
      <div className="grid max-w-[560px] grid-cols-1 gap-[10px] sm:grid-cols-2">
        {opt(
          "generate",
          "Generate for me",
          "A strong password is generated and shown once after creation.",
        )}
        {opt("set", "I'll set it", "Supply your own password (and username, if allowed).")}
      </div>
    </fieldset>
  );
}

function CredentialCard({ cred, errs }: { cred: WizardCredential; errs: WizardError[] }) {
  const s = useWizardStore();
  const patch = (p: Partial<WizardCredential>) =>
    s.set({ credentials: s.credentials.map((c) => (c.name === cred.name ? { ...c, ...p } : c)) });

  const pwErr = fieldError(errs, `credential:${cred.name}:password`);

  return (
    <div className="mb-4 max-w-[600px] rounded-fluent border border-line bg-card p-4">
      <div className="mb-1 flex items-center gap-2">
        <Mi name="person" size={15} color="var(--color-accent)" />
        <span className="text-[14px] font-semibold">{cred.name}</span>
      </div>

      {cred.fixedUsername && !cred.usernameSettable ? (
        <p className="mb-3 text-[12px] text-ink-2">
          Fixed username: <code className="font-mono">{cred.fixedUsername}</code>
        </p>
      ) : null}

      {cred.userSettable ? (
        <>
          <ModeRadio cred={cred} onMode={(mode) => patch({ mode })} />

          {cred.mode === "set" ? (
            <div className="mt-4">
              {cred.usernameSettable ? (
                <FormRow
                  label="Username"
                  help="Leave blank to use the service default."
                  error={fieldError(errs, `credential:${cred.name}:username`)}
                >
                  <Input
                    value={cred.username}
                    onChange={(e) => patch({ username: e.target.value })}
                    autoComplete="off"
                    aria-label={`${cred.name} username`}
                    className="w-[300px]"
                  />
                </FormRow>
              ) : null}
              <FormRow
                label="Password"
                required
                help="At least 12 characters. Any characters are allowed."
                error={pwErr}
              >
                <Input
                  type="password"
                  value={cred.password}
                  onChange={(e) => patch({ password: e.target.value })}
                  autoComplete="new-password"
                  aria-label={`${cred.name} password`}
                  invalid={!!pwErr && cred.password !== ""}
                  className="w-[300px]"
                />
              </FormRow>
            </div>
          ) : (
            <p className="mt-3 flex items-start gap-2 text-[12px] leading-[1.5] text-ink-2">
              <Mi name="info" size={13} color="var(--color-ink-2)" style={{ marginTop: 2 }} />
              <span>
                The generated value is shown once on the next screen — Proxcloud never stores it.
              </span>
            </p>
          )}
        </>
      ) : (
        <p className="flex items-start gap-2 text-[12px] leading-[1.5] text-ink-2">
          <Mi name="info" size={13} color="var(--color-ink-2)" style={{ marginTop: 2 }} />
          <span>
            This credential is generated automatically and shown once after creation. Proxcloud does
            not store it.
          </span>
        </p>
      )}
    </div>
  );
}

export function CredentialsTab({ errs }: { errs: WizardError[] }) {
  const s = useWizardStore();

  return (
    <div>
      <SectionHeading caption="Each credential is generated for you by default. Choose “I'll set it” to supply your own — anything you generate is shown only once and never stored.">
        Credentials
      </SectionHeading>

      {s.credentials.length === 0 ? (
        <p className="text-[13px] text-ink-2">
          This service declares no configurable credentials — nothing to set here.
        </p>
      ) : (
        s.credentials.map((c) => <CredentialCard key={c.name} cred={c} errs={errs} />)
      )}
    </div>
  );
}
