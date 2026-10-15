import { Link } from "react-router";
import { Empty } from "../components/ui";

export default function NotFound() {
  return (
    <Empty title="Page not found">
      <Link to="/" className="text-accent hover:underline">
        Go to the dashboard
      </Link>
    </Empty>
  );
}
