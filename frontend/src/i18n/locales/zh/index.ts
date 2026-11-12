import supportTickets from './supportTickets'
import supportChat from './supportChat'
import usageExports from './usageExports'
import adminAccessKeys from './adminAccessKeys'
import landing from './landing'
import common from './common'
import dashboard from './dashboard'
import channelMonitorV2 from './channelMonitorV2'
import batchImage from './batchImage'
import admin from './admin'
import misc from './misc'
import managementMcp from './managementMcp'
import mcp from './mcp'

export default {
  ...supportTickets,
  ...supportChat,
  usageExports,
  adminAccessKeys,
  ...landing,
  ...common,
  ...dashboard,
  ...channelMonitorV2,
  ...batchImage,
  admin,
  mcp,
  managementMcp,
  ...misc,
}
