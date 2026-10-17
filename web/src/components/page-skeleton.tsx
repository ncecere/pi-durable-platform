/* A placeholder page while a detail page (a team, a user) loads. */
import { Skeleton, SkeletonText } from "@/components/ui/skeleton/skeleton";
import s from "../pages/shared.module.css";
import p from "./page-skeleton.module.css";

export function PageSkeleton() {
  return (
    <div role="status" aria-label="Loading…" className={p.skeleton}>
      <Skeleton width="16rem" height="1.75rem" />
      <SkeletonText lines={2} />
      <div className={s.stats3}>
        <Skeleton height="6rem" />
        <Skeleton height="6rem" />
        <Skeleton height="6rem" />
      </div>
      <Skeleton height="14rem" />
    </div>
  );
}
