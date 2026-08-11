// Card — white surface, 1px #EDEBE9 border, radius 2, Fluent depth-4 shadow.
// hoverable elevates to depth-8 on hover (clickable cards only, §6).
import type { ReactNode } from "react";

export interface CardProps {
  children: ReactNode;
  hoverable?: boolean;
  className?: string;
}

export function Card({ children, hoverable = false, className = "" }: CardProps) {
  return (
    <div
      className={`rounded-fluent border border-line bg-card shadow-card ${
        hoverable ? "hover:shadow-card-hover" : ""
      } ${className}`}
    >
      {children}
    </div>
  );
}
