
import { Suspense, useEffect, useMemo, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Label } from "@/components/ui/label";
import { Checkbox } from "@/components/ui/checkbox";
import { Spinner } from "@/components/ui/spinner";
import { AlertCircle, Lock, Shield, User } from "lucide-react";
import { useI18n } from "@/lib/i18n";
import { useForm, type FormIssue } from "@/lib/hooks/useForm";
import { isLoginSuccessResponse, parseLoginErrorBody, safeNextPath } from "@/lib/login";
import { paths } from "@/lib/api-paths";
import TelemetryCollector from "@/components/TelemetryCollector";

type LoginFormValues = {
  username: string;
  password: string;
  rememberMe: boolean;
};

function LoginForm() {
  const { t } = useI18n();
  const [error, setError] = useState("");
  const [version, setVersion] = useState("");
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();

  // A hand-written schema instead of zod: the form has two `required` checks,
  // and importing zod pulled the 82 kB vendor-zod chunk into the public login
  // page (the largest avoidable script on the demo's front door). `useForm`
  // accepts any object with this safeParse shape.
  const loginSchema = useMemo(
    () => ({
      safeParse: (vals: LoginFormValues) => {
        const issues: FormIssue[] = [];
        if (!vals.username.trim()) {
          issues.push({ path: ["username"], message: t("login.username_required") });
        }
        if (!vals.password) {
          issues.push({ path: ["password"], message: t("login.password_required") });
        }
        return issues.length
          ? ({ success: false, error: { issues } } as const)
          : ({ success: true } as const);
      },
    }),
    [t],
  );

  const {
    values,
    errors,
    touched,
    isSubmitting,
    handleChange,
    handleBlur,
    handleSubmit,
    setFieldValue,
  } = useForm<LoginFormValues>({
    initialValues: { username: "", password: "", rememberMe: false },
    schema: loginSchema,
    onSubmit: async (vals) => {
      setError("");
      const params = new URLSearchParams();
      params.append("username", vals.username);
      params.append("password", vals.password);
      if (vals.rememberMe) params.append("remember_me", "on");

      try {
        const response = await fetch(paths.auth.login, {
          method: "POST",
          headers: {
            "Content-Type": "application/x-www-form-urlencoded",
            Accept: "application/json, text/html, */*",
          },
          body: params.toString(),
          credentials: "include",
          redirect: "manual",
        });

        if (isLoginSuccessResponse(response)) {
          const dest = safeNextPath(searchParams.get("next"));
          navigate(dest);
          window.location.reload();
          return;
        }

        let msg = t("login.error_failed");
        try {
          const ct = response.headers.get("content-type") || "";
          if (ct.includes("application/json")) {
            const data = await response.json();
            const parsed = parseLoginErrorBody(data);
            if (parsed) msg = parsed;
          }
        } catch { /* keep default */ }
        setError(msg);
      } catch {
        setError(t("login.error_network"));
      }
    },
  });

  useEffect(() => {
    if (searchParams.get("expired") === "1") {
      setError(t("login.session_expired"));
    }
  }, [searchParams, t]);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const res = await fetch(paths.auth.health, { credentials: "include" });
        if (!res.ok) return;
        const data = await res.json() as { version?: string };
        if (!cancelled && data.version) setVersion(String(data.version));
      } catch { /* ignore */ }
    })();
    return () => { cancelled = true; };
  }, []);

  return (
    <div className="grid min-h-screen overflow-hidden bg-background lg:grid-cols-[minmax(0,1.08fr)_minmax(28rem,0.92fr)]">
      <section className="relative hidden overflow-hidden border-r border-border bg-[linear-gradient(145deg,var(--surface-2),var(--surface-3))] p-12 lg:flex lg:flex-col lg:justify-between xl:p-16">
        <div aria-hidden="true" className="absolute inset-0 opacity-[0.3] [background-image:linear-gradient(var(--border)_1px,transparent_1px),linear-gradient(90deg,var(--border)_1px,transparent_1px)] [background-size:48px_48px] [mask-image:linear-gradient(to_bottom,black,transparent_90%)]" />
        <div aria-hidden="true" className="absolute -right-28 top-20 size-80 rounded-full bg-primary/8 blur-(--blur-orb)" />
        <div className="relative flex items-center gap-3">
          <div className="flex size-11 items-center justify-center rounded-xl bg-primary text-primary-foreground shadow-md shadow-primary/15">
            <Shield className="size-5" aria-hidden="true" />
          </div>
          <div>
            <div className="text-lg font-bold tracking-tight">Forge<span className="text-primary">C2</span></div>
            <div className="mono-eyebrow text-muted-foreground">Command platform</div>
          </div>
        </div>
        <div className="relative max-w-xl animate-fade-slide-up">
          <div className="mono-eyebrow mb-4 text-primary">{t("login.hero_eyebrow")}</div>
          <h1 className="max-w-lg text-4xl font-semibold leading-tight tracking-tight text-foreground xl:text-5xl">{t("login.hero_title")}</h1>
          <p className="mt-5 max-w-lg text-base leading-7 text-muted-foreground">{t("login.hero_description")}</p>
          <div className="mt-9 grid gap-3 sm:grid-cols-3">
            {[t("login.feature_operations"), t("login.feature_visibility"), t("login.feature_control")].map((feature) => (
              <div key={feature} className="flex items-center gap-2 rounded-lg border border-border/80 bg-card/75 px-3 py-2.5 text-sm font-medium shadow-sm">
                <span className="size-1.5 rounded-full bg-primary" />{feature}
              </div>
            ))}
          </div>
        </div>
        <p className="relative text-xs text-muted-foreground">{t("login.footer")}</p>
      </section>

      <section className="relative flex min-h-screen items-center justify-center overflow-hidden bg-muted/20 px-5 py-10 sm:px-10">
        <div aria-hidden="true" className="absolute -right-24 -top-24 size-80 rounded-full bg-primary/6 blur-(--blur-orb)" />
        <div aria-hidden="true" className="absolute -bottom-32 -left-24 size-72 rounded-full bg-primary/4 blur-(--blur-orb)" />
        <div className="relative z-10 w-full max-w-[27rem] animate-fade-slide-up">
          <div className="mb-7 lg:hidden">
            <div className="mb-4 flex size-12 items-center justify-center rounded-xl bg-primary text-primary-foreground shadow-md shadow-primary/15">
              <Shield className="size-5" aria-hidden="true" />
            </div>
            <h1 className="text-2xl font-bold tracking-tight text-foreground">Forge<span className="text-primary">C2</span></h1>
            <p className="mt-1 text-sm text-muted-foreground">{t("login.subtitle")}</p>
          </div>

        <Card className="border-border/90 bg-card shadow-lg shadow-foreground/5">
          <CardHeader className="border-b border-border/70 pb-5">
            <div className="flex items-start justify-between gap-4">
              <div>
                <h2 className="text-xl font-semibold tracking-tight text-foreground">{t("login.form_title")}</h2>
                <p className="mt-1.5 text-sm leading-6 text-muted-foreground">{t("login.form_description")}</p>
              </div>
              <div className="hidden size-10 shrink-0 items-center justify-center rounded-xl border border-primary/15 bg-primary/10 text-primary sm:flex" aria-hidden="true">
                <Lock className="size-4" />
              </div>
            </div>
          </CardHeader>
          <CardContent className="px-6 sm:px-7">
          {error && (
            <Alert variant="destructive" className="mb-5 animate-scale-in" role="alert">
              <AlertCircle className="size-4" aria-hidden="true" />
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}

          <form onSubmit={handleSubmit} className="space-y-5" aria-label={t("login.form_aria")} noValidate>
            <div>
              <Label htmlFor="login-username" className="mb-2 block text-sm text-foreground">{t("login.username")}</Label>
              <div className="relative group">
                <User className="absolute left-3 top-1/2 -translate-y-1/2 size-4 text-muted-foreground pointer-events-none z-10 transition-colors group-focus-within:text-primary" aria-hidden="true" />
                <Input
                  id="login-username"
                  type="text"
                  autoFocus
                  value={values.username}
                  onChange={handleChange("username")}
                  onBlur={handleBlur("username")}
                  className="h-11 pl-9 transition-colors"
                  aria-invalid={!!(touched.username && errors.username)}
                  aria-describedby={errors.username ? "login-username-error" : undefined}
                  autoComplete="username"
                />
              </div>
              {touched.username && errors.username && (
                <p id="login-username-error" role="alert" className="text-xs text-destructive-ink mt-1">{errors.username}</p>
              )}
            </div>

            <div>
              <Label htmlFor="login-password" className="mb-2 block text-sm text-foreground">{t("login.password")}</Label>
              <div className="relative group">
                <Lock className="absolute left-3 top-1/2 -translate-y-1/2 size-4 text-muted-foreground pointer-events-none z-10 transition-colors group-focus-within:text-primary" aria-hidden="true" />
                <Input
                  id="login-password"
                  type="password"
                  value={values.password}
                  onChange={handleChange("password")}
                  onBlur={handleBlur("password")}
                  className="h-11 pl-9 transition-colors"
                  aria-invalid={!!(touched.password && errors.password)}
                  aria-describedby={errors.password ? "login-password-error" : undefined}
                  autoComplete="current-password"
                />
              </div>
              {touched.password && errors.password && (
                <p id="login-password-error" role="alert" className="text-xs text-destructive-ink mt-1">{errors.password}</p>
              )}
            </div>

            <label className="flex min-h-11 cursor-pointer select-none items-center gap-2.5 rounded-lg px-1">
              <Checkbox
                checked={values.rememberMe}
                onCheckedChange={(v) => setFieldValue("rememberMe", v === true)}
                aria-label={t("login.remember_me")}
              />
              <span className="text-sm text-muted-foreground">{t("login.remember_me")}</span>
            </label>

            <Button
              type="submit"
              variant="default"
              size="lg"
              disabled={isSubmitting}
              className="w-full font-semibold"
            >
              {isSubmitting ? (
                <span className="flex items-center justify-center gap-2">
                  <Spinner size="sm" />
                  {t("login.signing_in")}
                </span>
              ) : t("login.sign_in")}
            </Button>
          </form>
          </CardContent>
        </Card>

        <p className="mt-6 text-center text-xs text-muted-foreground">
          {version ? `${t("login.footer_version", { version })} · ${t("login.footer")}` : t("login.footer")}
        </p>
        </div>
      </section>
    </div>
  );
}

export default function LoginPage() {
  return (
    <Suspense fallback={<div className="min-h-screen flex items-center justify-center"><Spinner /></div>}>
      <TelemetryCollector />
      <LoginForm />
    </Suspense>
  );
}
