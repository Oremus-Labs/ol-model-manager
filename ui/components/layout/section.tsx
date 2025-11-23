import { ReactNode } from 'react';

interface SectionProps {
  title: string;
  description?: string;
  action?: ReactNode;
  children: ReactNode;
  id?: string;
}

export function Section({ title, description, action, children, id }: SectionProps) {
  return (
    <section id={id} className="space-y-4 rounded-3xl border border-white/5 bg-white/5 p-6 shadow-card backdrop-blur">
      <header className="flex flex-col gap-2 md:flex-row md:items-center md:justify-between">
        <div>
          <h2 className="text-lg font-semibold text-white">{title}</h2>
          {description && <p className="text-sm text-slate-300">{description}</p>}
        </div>
        {action && <div className="text-sm text-slate-200">{action}</div>}
      </header>
      <div className="space-y-4">{children}</div>
    </section>
  );
}
