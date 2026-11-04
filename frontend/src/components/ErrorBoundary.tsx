
import { Component, ErrorInfo, ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { Bug } from "lucide-react";
import { I18nFallbackProvider, useI18n } from "@/lib/i18n";
import { logger } from "@/lib/logger";

interface Props {
  children: ReactNode;
  fallback?: ReactNode;
  /** When this value changes, a latched error state resets. Route wrappers
   *  pass the pathname so navigating away from a crashed page recovers. */
  resetKey?: string | number;
}

interface State {
  hasError: boolean;
  error: Error | null;
}

class ErrorBoundaryInner extends Component<Props, State> {
  constructor(props: Props) {
    super(props);
    this.state = { hasError: false, error: null };
  }

  static getDerivedStateFromError(error: Error): State {
    return { hasError: true, error };
  }

  // G12 fix: always log caught errors (not only in dev) so that the
  // TelemetryCollector's window.error handler can capture them in production.
  componentDidCatch(error: Error, info: ErrorInfo) {
    logger.error("ErrorBoundary caught error", error, { componentStack: info.componentStack });
  }

  componentDidUpdate(prevProps: Props) {
    if (this.state.hasError && prevProps.resetKey !== this.props.resetKey) {
      this.setState({ hasError: false, error: null });
    }
  }

  render() {
    if (this.state.hasError) {
      return <I18nFallbackProvider>
        {this.props.fallback !== undefined ? this.props.fallback :
          <ErrorFallbackUI onReset={() => this.setState({ hasError: false, error: null })} />}
      </I18nFallbackProvider>;
    }
    return this.props.children;
  }
}

function ErrorFallbackUI({ onReset }: { onReset: () => void }) {
  const { t } = useI18n();
  return (
    <div role="alert" className="flex flex-col items-center justify-center min-h-[300px] p-8 text-center">
      <div className="mb-4 flex size-14 items-center justify-center rounded-lg bg-destructive/10">
        <Bug aria-hidden="true" className="size-7 text-destructive-ink" />
      </div>
      <h2 className="text-lg font-semibold text-foreground mb-2">{t("error.boundary_title")}</h2>
      <p className="text-sm text-muted-foreground mb-4 max-w-md">
        {t("error.boundary_message")}
      </p>
      <div className="flex flex-wrap justify-center gap-2">
        <Button size="sm" variant="outline" onClick={onReset}>
          {t("error.try_again")}
        </Button>
        <Button size="sm" onClick={() => window.location.reload()}>
          {t("error.reload_page")}
        </Button>
      </div>
    </div>
  );
}

export default function ErrorBoundary(props: Props) {
  return <ErrorBoundaryInner {...props} />;
}

