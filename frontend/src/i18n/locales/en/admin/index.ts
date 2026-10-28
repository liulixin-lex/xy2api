import overview from './overview'
import channels from './channels'
import accounts from './accounts'
import resources from './resources'
import ops from './ops'
import settings from './settings'
import audit from './audit'
import promptAudit from './promptAudit'
import plugins from './plugins'
import shareScheduling from './shareScheduling'
import resilience from './resilience'
import groupCategories from './groupCategories'
import operatorAccess from './operatorAccess'

export default {
  ...overview,
  ...channels,
  ...accounts,
  ...resources,
  ...ops,
  ...settings,
  ...audit,
  ...promptAudit,
  ...plugins,
  ...shareScheduling,
  ...resilience,
  ...groupCategories,
  ...operatorAccess,
}
